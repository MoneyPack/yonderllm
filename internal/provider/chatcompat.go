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
	"slices"
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
	extraHeaders      map[string]string
	omitStreamOptions bool
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
	// Copy the injected client instead of changing a caller-owned client.
	client := *p.client
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	p.client = &client
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

// WithoutStreamOptions supports servers which reject the optional usage field.
func WithoutStreamOptions() ChatOption {
	return func(p *ChatCompat) { p.omitStreamOptions = true }
}

// Name reports the provider's stable identifier.
func (p *ChatCompat) Name() string { return p.name }

// chatRequest is the wire shape of a completion request. Optional fields are
// pointers or carry omitempty, because some servers reject an explicit null
// where they would accept an absent key.
type chatRequest struct {
	// Model carries omitempty as a last line of defence: the session refuses
	// to call a provider with no model configured, so an empty value here
	// means a bug. Sending no key at all lets the server answer with its own
	// complaint rather than hunting for a model literally named "".
	Model       string        `json:"model,omitempty"`
	Messages    []wireMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	Stream      bool          `json:"stream"`
	// Tools is omitted entirely when empty rather than sent as an empty
	// array, because a server that sees the key at all may switch the model
	// onto a tool-aware prompt template it does not need.
	Tools []wireTool `json:"tools,omitempty"`
	// StreamOptions asks for a final usage frame. Servers that do not know
	// the field ignore it.
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// wireTool is a tool in the envelope the format requires. The "function" nesting
// is vestigial — the type has only ever had the one value — but servers still
// validate it, so we send it.
type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls carries what the model asked for on an assistant turn we
	// are replaying back to it, and ToolCallID ties a tool result to the
	// call it answers. Both are absent on ordinary turns.
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireCallFunction `json:"function"`
}

type wireCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// toolCallFragment is one piece of a streamed tool call. A server sends the
// call's id and name in one frame and its arguments a few characters at a time
// in the frames that follow, so no single fragment is usable on its own.
type toolCallFragment struct {
	// Index numbers the call within the message, so that fragments of two
	// tools requested at once can be told apart. A server streaming a
	// single call sometimes leaves it out, hence the pointer.
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// chatChunk is one SSE frame of a streamed completion.
type chatChunk struct {
	Error   json.RawMessage `json:"error"`
	Choices []struct {
		Delta struct {
			Content   string             `json:"content"`
			ToolCalls []toolCallFragment `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// toolCallBuffer reassembles the fragments of a streamed tool call.
//
// It exists so that [Chunk.ToolCalls] can promise complete calls: a caller has
// nothing to do with half an argument list, and every caller would otherwise
// have to repeat this bookkeeping for itself.
type toolCallBuffer struct {
	order []int
	calls map[int]*ToolCall
}

func newToolCallBuffer() *toolCallBuffer {
	return &toolCallBuffer{calls: map[int]*ToolCall{}}
}

// add folds one fragment into the call it belongs to.
func (b *toolCallBuffer) add(f toolCallFragment) {
	i := b.slot(f)
	call, ok := b.calls[i]
	if !ok {
		call = &ToolCall{}
		b.calls[i] = call
		b.order = append(b.order, i)
	}
	// Identity arrives once and the rest of the fragments repeat nothing,
	// so an empty field is silence rather than a correction.
	if f.ID != "" {
		call.ID = f.ID
	}
	if f.Function.Name != "" {
		call.Name = f.Function.Name
	}
	call.Arguments += f.Function.Arguments
}

// slot picks the call a fragment belongs to. An unnumbered fragment starts a
// new call if it introduces an id and otherwise continues the one in progress,
// which is how a server that streams a single call and omits the numbering
// still reassembles correctly.
func (b *toolCallBuffer) slot(f toolCallFragment) int {
	switch {
	case f.Index != nil:
		return *f.Index
	case f.ID != "" || len(b.order) == 0:
		return len(b.order)
	default:
		return b.order[len(b.order)-1]
	}
}

// take returns the reassembled calls and empties the buffer.
//
// They come back ordered by the server's own numbering rather than by the order
// the fragments arrived, so that a provider which interleaves two calls still
// hands the caller the sequence the model wrote.
func (b *toolCallBuffer) take() []ToolCall {
	if len(b.order) == 0 {
		return nil
	}
	slots := slices.Clone(b.order)
	slices.Sort(slots)
	out := make([]ToolCall, 0, len(slots))
	for _, i := range slots {
		out = append(out, *b.calls[i])
	}
	b.order = nil
	b.calls = map[int]*ToolCall{}
	return out
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
		if p.omitStreamOptions {
			body.StreamOptions = nil
		}
		for _, t := range req.Tools {
			body.Tools = append(body.Tools, wireTool{
				Type: "function",
				Function: wireFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.Parameters,
				},
			})
		}
		for _, m := range req.Messages {
			wm := wireMessage{
				Role:       string(m.Role),
				Content:    m.Content,
				ToolCallID: m.ToolCallID,
			}
			for _, c := range m.ToolCalls {
				wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
					ID:   c.ID,
					Type: "function",
					Function: wireCallFunction{
						Name:      c.Name,
						Arguments: c.Arguments,
					},
				})
			}
			body.Messages = append(body.Messages, wm)
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

		calls := newToolCallBuffer()
		streamBytes := 0
		finished := false
		// flush emits whatever calls have been reassembled but not yet
		// handed over. It runs at an explicit [DONE] without a finish
		// reason, which happens on servers that go straight from the last
		// argument fragment to [DONE]; without it those calls would be
		// collected and then dropped.
		flush := func() bool {
			pending := calls.take()
			if len(pending) == 0 {
				return true
			}
			return yield(Chunk{ToolCalls: pending, Finish: FinishTool}, nil)
		}

		for scanner.Scan() {
			streamBytes += len(scanner.Bytes()) + 1
			if streamBytes > 16*1024*1024 {
				yield(Chunk{}, fmt.Errorf("%s: response stream exceeds the 16 MiB limit", p.name))
				return
			}
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
				flush()
				return
			}

			var frame chatChunk
			if err := json.Unmarshal([]byte(data), &frame); err != nil {
				yield(Chunk{}, fmt.Errorf("%s: decoding stream frame: %w", p.name, err))
				return
			}
			if len(frame.Error) > 0 && string(frame.Error) != "null" {
				var detail struct {
					Message string `json:"message"`
				}
				_ = json.Unmarshal(frame.Error, &detail)
				message := p.redactMessage(detail.Message)
				if message == "" {
					message = "provider reported an error in the response stream"
				}
				yield(Chunk{}, fmt.Errorf("%s: stream error: %s", p.name, message))
				return
			}

			var chunk Chunk
			if len(frame.Choices) > 0 {
				choice := frame.Choices[0]
				chunk.Delta = choice.Delta.Content
				chunk.Finish = finishReason(choice.FinishReason)
				for _, f := range choice.Delta.ToolCalls {
					calls.add(f)
				}
				// A finish reason closes the model's turn, so anything
				// collected by now is whole and goes out with it. The
				// reason is restated as FinishTool even when the server
				// called it a plain stop, because the turn is not over
				// for the caller: there are calls left to run.
				if chunk.Finish != FinishNone {
					finished = true
					if pending := calls.take(); len(pending) > 0 {
						chunk.ToolCalls = pending
						chunk.Finish = FinishTool
					}
				}
			}
			if frame.Usage != nil {
				chunk.Usage = &Usage{
					PromptTokens:     frame.Usage.PromptTokens,
					CompletionTokens: frame.Usage.CompletionTokens,
				}
			}
			// Frames that carry nothing at all (an empty role-priming delta,
			// for instance) would only make the caller redraw for no reason.
			if chunk.empty() {
				continue
			}
			if !yield(chunk, nil) {
				return
			}
		}

		if err := scanner.Err(); err != nil {
			yield(Chunk{}, fmt.Errorf("%s: reading stream: %w", p.name, err))
			return
		}
		if !finished {
			yield(Chunk{}, fmt.Errorf("%s: response stream ended before completion: %w", p.name, io.ErrUnexpectedEOF))
			return
		}
	}
}

// finishReason maps the wire vocabulary onto ours. An unrecognized value is
// reported as FinishStop: the response did end, and we know nothing worse.
func finishReason(s string) FinishReason {
	switch s {
	case "":
		return FinishNone
	case "stop":
		return FinishStop
	case "tool_calls", "function_call":
		return FinishTool
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
		// Name is the human-readable label. Not every provider publishes
		// one, so the ID stands in when it is missing.
		Name string `json:"name"`
		// Providers disagree on the name of the window field, so both
		// spellings we have seen are accepted.
		ContextWindow int `json:"context_window"`
		ContextLength int `json:"context_length"`
		// Pricing quotes per-token rates as decimal strings, small enough
		// that JSON numbers would arrive in exponent form. Providers that
		// publish no prices simply omit the object.
		Pricing struct {
			Prompt     string `json:"prompt"`
			Completion string `json:"completion"`
		} `json:"pricing"`
	} `json:"data"`
}

// parsePricing reads the quoted per-token rates for one catalogue entry.
//
// Both rates must be present and parse for the result to count as known. A
// listing that quotes only one of them tells us too little to band the model:
// filling the other in as zero would read as free or cheap on no evidence,
// whereas [TierUnknown] says exactly what we know and still shows the model.
func parsePricing(prompt, completion string) Pricing {
	if prompt == "" || completion == "" {
		return Pricing{}
	}
	in, err := strconv.ParseFloat(prompt, 64)
	if err != nil {
		return Pricing{}
	}
	out, err := strconv.ParseFloat(completion, 64)
	if err != nil {
		return Pricing{}
	}
	return Pricing{Known: true, Prompt: in, Completion: out}
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
		name := m.Name
		if name == "" {
			name = m.ID
		}
		models = append(models, Model{
			ID:            m.ID,
			Name:          name,
			ContextWindow: window,
			Pricing:       parsePricing(m.Pricing.Prompt, m.Pricing.Completion),
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
	message := p.redactMessage(envelope.Error.Message)

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

	// A 5xx is the provider's own fault, not the request's, so the next
	// provider in the chain is worth trying. A 4xx we have not named above is
	// a bad request, and every provider would reject it the same way, so it
	// stays an untyped error that ends the turn.
	if resp.StatusCode >= 500 {
		return &UnavailableError{Provider: p.name, Status: resp.StatusCode, Reason: message}
	}

	return fmt.Errorf("%s: %s (HTTP %d)", p.name, message, resp.StatusCode)
}

func (p *ChatCompat) redactMessage(message string) string {
	message = strings.TrimSpace(message)
	if p.apiKey != "" {
		message = strings.ReplaceAll(message, p.apiKey, "[redacted]")
	}
	for _, value := range p.extraHeaders {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[redacted]")
		}
	}
	return redactKeyish(message)
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
