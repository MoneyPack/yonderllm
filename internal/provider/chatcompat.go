package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ChatCompat adapts any backend that speaks the [OI]-style chat-completions
// wire format. Groq, OpenRouter, Together, and most local servers all do, so
// they share one transport rather than one copy of SSE parsing each.
type ChatCompat struct {
	name    string
	baseURL string
	apiKey  string
	client  *http.Client
	// extraHeaders are sent on every request. OpenRouter, for instance, asks
	// callers to identify themselves via HTTP-Referer and X-Title.
	extraHeaders map[string]string
}

// NewChatCompat builds an adapter. baseURL must already include any version
// segment, e.g. https://api.groq.com/openai/v1.
func NewChatCompat(name, baseURL, apiKey string, opts ...ChatOption) *ChatCompat {
	p := &ChatCompat{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		// No overall client timeout: a long generation is not a hung
		// connection. Cancellation is the caller's job, via the context.
		client:       &http.Client{},
		extraHeaders: map[string]string{},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// ChatOption customizes an adapter at construction.
type ChatOption func(*ChatCompat)

// WithHTTPClient supplies a client, so tests can point at a stub server and
// callers can install their own transport.
func WithHTTPClient(c *http.Client) ChatOption {
	return func(p *ChatCompat) { p.client = c }
}

// WithHeader adds a header sent on every request.
func WithHeader(key, value string) ChatOption {
	return func(p *ChatCompat) { p.extraHeaders[key] = value }
}

// Name reports the provider's stable identifier.
func (p *ChatCompat) Name() string { return p.name }

// chatRequest is the wire shape of a completion request. Optional fields are
// pointers or carry omitempty, because some servers reject an explicit null
// where they would accept an absent key.
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []wireMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	Stream      bool          `json:"stream"`
	// StreamOptions asks for a final usage frame. Servers that do not know
	// the field ignore it.
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatChunk is one SSE frame of a streamed completion.
type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Stream sends req and yields chunks as the server produces them.
func (p *ChatCompat) Stream(ctx context.Context, req Request) iter.Seq2[Chunk, error] {
	return func(yield func(Chunk, error) bool) {
		body := chatRequest{
			Model:         req.Model,
			Messages:      make([]wireMessage, 0, len(req.Messages)),
			MaxTokens:     req.MaxTokens,
			Temperature:   req.Temperature,
			Stream:        true,
			StreamOptions: &streamOptions{IncludeUsage: true},
		}
		for _, m := range req.Messages {
			body.Messages = append(body.Messages, wireMessage{
				Role:    string(m.Role),
				Content: m.Content,
			})
		}

		encoded, err := json.Marshal(body)
		if err != nil {
			yield(Chunk{}, fmt.Errorf("%s: encoding request: %w", p.name, err))
			return
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			p.baseURL+"/chat/completions", bytes.NewReader(encoded))
		if err != nil {
			yield(Chunk{}, fmt.Errorf("%s: building request: %w", p.name, err))
			return
		}
		p.setHeaders(httpReq)
		httpReq.Header.Set("Accept", "text/event-stream")

		resp, err := p.client.Do(httpReq)
		if err != nil {
			yield(Chunk{}, fmt.Errorf("%s: %w", p.name, err))
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			yield(Chunk{}, p.statusError(resp))
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		// A single SSE frame can carry a large delta; the default 64 KiB
		// limit is not generous enough to rely on.
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			// Blank separators and comment/keep-alive lines carry no data.
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}
			data, ok := strings.CutPrefix(line, "data:")
			if !ok {
				continue
			}
			data = strings.TrimSpace(data)
			if data == "[DONE]" {
				return
			}

			var frame chatChunk
			if err := json.Unmarshal([]byte(data), &frame); err != nil {
				yield(Chunk{}, fmt.Errorf("%s: decoding stream frame: %w", p.name, err))
				return
			}

			var chunk Chunk
			if len(frame.Choices) > 0 {
				chunk.Delta = frame.Choices[0].Delta.Content
				chunk.Finish = finishReason(frame.Choices[0].FinishReason)
			}
			if frame.Usage != nil {
				chunk.Usage = &Usage{
					PromptTokens:     frame.Usage.PromptTokens,
					CompletionTokens: frame.Usage.CompletionTokens,
				}
			}
			// Frames that carry nothing at all (an empty role-priming delta,
			// for instance) would only make the caller redraw for no reason.
			if chunk == (Chunk{}) {
				continue
			}
			if !yield(chunk, nil) {
				return
			}
		}

		if err := scanner.Err(); err != nil {
			yield(Chunk{}, fmt.Errorf("%s: reading stream: %w", p.name, err))
		}
	}
}

// finishReason maps the wire vocabulary onto ours. An unrecognized value is
// reported as FinishStop: the response did end, and we know nothing worse.
func finishReason(s string) FinishReason {
	switch s {
	case "":
		return FinishNone
	case "stop", "tool_calls", "function_call":
		return FinishStop
	case "length", "max_tokens":
		return FinishLength
	case "content_filter":
		return FinishFilter
	default:
		return FinishStop
	}
}

// modelList is the wire shape of GET /models.
type modelList struct {
	Data []struct {
		ID string `json:"id"`
		// Providers disagree on the name of the window field, so both
		// spellings we have seen are accepted.
		ContextWindow int `json:"context_window"`
		ContextLength int `json:"context_length"`
	} `json:"data"`
}

// Models enumerates what the backend offers.
func (p *ChatCompat) Models(ctx context.Context) ([]Model, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("%s: building request: %w", p.name, err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, p.statusError(resp)
	}

	var list modelList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("%s: decoding model list: %w", p.name, err)
	}

	models := make([]Model, 0, len(list.Data))
	for _, m := range list.Data {
		window := m.ContextWindow
		if window == 0 {
			window = m.ContextLength
		}
		models = append(models, Model{
			ID:            m.ID,
			Name:          m.ID,
			ContextWindow: window,
			// Whether a model is free depends on the account's tier, which
			// the listing does not report, so this stays false here.
		})
	}
	return models, nil
}

func (p *ChatCompat) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	for k, v := range p.extraHeaders {
		req.Header.Set(k, v)
	}
}

// statusError converts a non-200 response into a typed error, so the core can
// distinguish a quota stop worth falling back from a bad credential worth
// reporting. The provider's message is redacted before it is surfaced, because
// several providers echo the offending key back on a 401.
func (p *ChatCompat) statusError(resp *http.Response) error {
	// Cap the read: an error body should be small, and a broken or hostile
	// server should not be able to make us buffer without bound.
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))

	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	message := redactKeyish(strings.TrimSpace(envelope.Error.Message))

	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return &QuotaError{Provider: p.name, RetryAfter: retryAfter(resp)}
	case http.StatusPaymentRequired:
		// Some gateways bill an exhausted free tier as 402 rather than 429.
		return &QuotaError{Provider: p.name, RetryAfter: retryAfter(resp)}
	case http.StatusUnauthorized, http.StatusForbidden:
		return &AuthError{Provider: p.name, Reason: message}
	}

	if message == "" {
		message = strings.TrimSpace(resp.Status)
	}
	return fmt.Errorf("%s: %s (HTTP %d)", p.name, message, resp.StatusCode)
}

// retryAfter reads the standard hint, accepting both the seconds and the HTTP
// date form. It returns 0 when the header is absent or unparseable.
func retryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// redactKeyish removes anything shaped like a credential from provider text.
func redactKeyish(s string) string {
	fields := strings.Fields(s)
	for i, f := range fields {
		trimmed := strings.Trim(f, `"'.,:;()[]`)
		if trimmed != "" && looksLikeKey(trimmed) {
			fields[i] = strings.ReplaceAll(f, trimmed, "[redacted]")
		}
	}
	return strings.Join(fields, " ")
}

// looksLikeKey reports whether a token resembles an API key: anything carrying
// a known vendor prefix, or a long opaque run of key characters.
func looksLikeKey(s string) bool {
	for _, prefix := range []string{"sk-", "sk_", "gsk_", "AIza", "or-"} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	if len(s) < 24 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
