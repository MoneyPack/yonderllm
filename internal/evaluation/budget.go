package evaluation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
)

// Budget reserves a conservative token-cost estimate before each dispatch.
// It never refunds reservations, even on failure/cancellation, because the
// provider may bill work not represented in the last usage event.
type Budget struct {
	Transport                            http.RoundTripper
	LimitUSD, PromptRate, CompletionRate float64
	mu                                   sync.Mutex
	reserved                             float64
	requests                             int
}

func (b *Budget) ReservedUSD() float64 { b.mu.Lock(); defer b.mu.Unlock(); return b.reserved }

func (b *Budget) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost {
		return nil, fmt.Errorf("evaluation budget only allows completion requests")
	}
	if b.LimitUSD <= 0 || b.PromptRate < 0 || b.CompletionRate < 0 || math.IsNaN(b.LimitUSD+b.PromptRate+b.CompletionRate) || math.IsInf(b.LimitUSD+b.PromptRate+b.CompletionRate, 0) {
		return nil, fmt.Errorf("evaluation budget requires finite nonnegative prices and positive limit")
	}
	const maxRequestBytes = 64 << 10
	data, err := io.ReadAll(io.LimitReader(req.Body, maxRequestBytes+1))
	closeErr := req.Body.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(data) > maxRequestBytes {
		return nil, fmt.Errorf("evaluation request exceeds budget size limit")
	}
	var request struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(data, &request); err != nil || request.MaxTokens <= 0 {
		return nil, fmt.Errorf("evaluation requires a positive max_tokens limit")
	}
	// UTF-8 wire bytes upper-bound byte-level token counts for our synthetic
	// tasks, with extra framing allowance. Provider prices/caps are external.
	cost := float64(len(data)+1024)*b.PromptRate + float64(request.MaxTokens)*b.CompletionRate
	b.mu.Lock()
	if b.requests >= 25 || b.reserved+cost > b.LimitUSD {
		b.mu.Unlock()
		return nil, fmt.Errorf("evaluation budget exhausted before dispatch")
	}
	b.reserved += cost
	b.requests++
	b.mu.Unlock()
	req.Body = io.NopCloser(bytes.NewReader(data))
	transport := b.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return transport.RoundTrip(req)
}
