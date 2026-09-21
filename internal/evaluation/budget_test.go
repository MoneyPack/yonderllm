package evaluation

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type budgetTransport struct{ calls int }

func (t *budgetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls++
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
}
func TestBudgetRefusesBeforeNetworkAndDoesNotRefund(t *testing.T) {
	transport := &budgetTransport{}
	b := &Budget{Transport: transport, LimitUSD: 0.003, PromptRate: 0.000001, CompletionRate: 0.000001}
	for i := 0; i < 3; i++ {
		req, err := http.NewRequest(http.MethodPost, "https://example.invalid/chat/completions", strings.NewReader(`{"max_tokens":1000}`))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := b.RoundTrip(req)
		if i == 0 {
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
		} else if err == nil {
			resp.Body.Close()
			t.Fatal("exhausted budget dispatched")
		}
	}
	if transport.calls != 1 || b.ReservedUSD() <= 0 {
		t.Fatalf("calls=%d reserved=%f", transport.calls, b.ReservedUSD())
	}
}
