package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// sseServer serves a fixed event stream on /chat/completions and records the
// decoded request body so tests can assert on what the adapter sent.
func sseServer(t *testing.T, frames []string) (*httptest.Server, *chatRequest) {
	t.Helper()
	var seen chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &seen); err != nil {
			t.Errorf("request body is not valid JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, f := range frames {
			_, _ = io.WriteString(w, f)
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func frame(payload string) string { return "data: " + payload + "\n\n" }

// collect drains a stream into its text, its final chunk, and its error.
func collect(seq func(func(Chunk, error) bool)) (string, []Chunk, error) {
	var text strings.Builder
	var chunks []Chunk
	var err error
	for c, e := range seq {
		if e != nil {
			err = e
			break
		}
		text.WriteString(c.Delta)
		chunks = append(chunks, c)
	}
	return text.String(), chunks, err
}

func TestStreamAssemblesDeltas(t *testing.T) {
	srv, seen := sseServer(t, []string{
		// A role-priming frame with no content: the adapter should drop it
		// rather than wake the caller for nothing.
		frame(`{"choices":[{"delta":{"role":"assistant"}}]}`),
		frame(`{"choices":[{"delta":{"content":"Hello"}}]}`),
		frame(`{"choices":[{"delta":{"content":", world"}}]}`),
		frame(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`),
		frame(`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":4}}`),
		"data: [DONE]\n\n",
	})

	p := NewChatCompat("stub", srv.URL, "gsk_secret", WithHTTPClient(srv.Client()))
	text, chunks, err := collect(p.Stream(context.Background(), Request{
		Model:     "tiny",
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
		MaxTokens: 64,
	}))
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if text != "Hello, world" {
		t.Errorf("assembled text = %q, want %q", text, "Hello, world")
	}
	if len(chunks) != 4 {
		t.Fatalf("got %d chunks, want 4 (empty priming frame should be dropped)", len(chunks))
	}
	if got := chunks[2].Finish; got != FinishStop {
		t.Errorf("finish reason = %q, want %q", got, FinishStop)
	}
	last := chunks[3]
	if last.Usage == nil {
		t.Fatal("final chunk carried no usage")
	}
	if last.Usage.PromptTokens != 11 || last.Usage.CompletionTokens != 4 {
		t.Errorf("usage = %+v, want prompt 11 completion 4", *last.Usage)
	}

	if seen.Model != "tiny" || !seen.Stream {
		t.Errorf("sent model %q stream %v, want tiny/true", seen.Model, seen.Stream)
	}
	if seen.StreamOptions == nil || !seen.StreamOptions.IncludeUsage {
		t.Error("adapter did not request the usage trailer")
	}
	if len(seen.Messages) != 1 || seen.Messages[0].Role != "user" {
		t.Errorf("sent messages = %+v, want one user message", seen.Messages)
	}
}

func TestStreamStopsAtDoneSentinel(t *testing.T) {
	srv, _ := sseServer(t, []string{
		frame(`{"choices":[{"delta":{"content":"kept"}}]}`),
		"data: [DONE]\n\n",
		// Anything past the sentinel is not part of the response.
		frame(`{"choices":[{"delta":{"content":"dropped"}}]}`),
	})

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	text, _, err := collect(p.Stream(context.Background(), Request{Model: "tiny"}))
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if text != "kept" {
		t.Errorf("text = %q, want %q", text, "kept")
	}
}

func TestStreamIgnoresCommentsAndBlankLines(t *testing.T) {
	srv, _ := sseServer(t, []string{
		": keep-alive\n\n",
		"\n",
		"event: message\n",
		frame(`{"choices":[{"delta":{"content":"ok"}}]}`),
		"data: [DONE]\n\n",
	})

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	text, _, err := collect(p.Stream(context.Background(), Request{Model: "tiny"}))
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if text != "ok" {
		t.Errorf("text = %q, want %q", text, "ok")
	}
}

func TestStreamSendsAuthAndExtraHeaders(t *testing.T) {
	var auth, title string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		title = r.Header.Get("X-Title")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewChatCompat("stub", srv.URL, "sk-abc",
		WithHTTPClient(srv.Client()), WithHeader("X-Title", "yonderllm"))
	if _, _, err := collect(p.Stream(context.Background(), Request{Model: "tiny"})); err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if auth != "Bearer sk-abc" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer sk-abc")
	}
	if title != "yonderllm" {
		t.Errorf("X-Title = %q, want %q", title, "yonderllm")
	}
}

func TestStreamCallerCanStopEarly(t *testing.T) {
	srv, _ := sseServer(t, []string{
		frame(`{"choices":[{"delta":{"content":"one"}}]}`),
		frame(`{"choices":[{"delta":{"content":"two"}}]}`),
		"data: [DONE]\n\n",
	})

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	seen := 0
	for c, err := range p.Stream(context.Background(), Request{Model: "tiny"}) {
		if err != nil {
			t.Fatalf("stream failed: %v", err)
		}
		seen++
		_ = c
		break
	}
	if seen != 1 {
		t.Errorf("consumed %d chunks after break, want 1", seen)
	}
}

func TestStreamQuotaErrorCarriesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limit reached","type":"rate_limit"}}`)
	}))
	defer srv.Close()

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	_, _, err := collect(p.Stream(context.Background(), Request{Model: "tiny"}))
	if !errors.Is(err, ErrQuota) {
		t.Fatalf("error = %v, want a quota error", err)
	}
	var qe *QuotaError
	if !errors.As(err, &qe) {
		t.Fatalf("error %v is not a *QuotaError", err)
	}
	if qe.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", qe.RetryAfter)
	}
	if qe.Provider != "stub" {
		t.Errorf("Provider = %q, want %q", qe.Provider, "stub")
	}
}

func TestStreamPaymentRequiredIsQuota(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(w, `{"error":{"message":"credits exhausted"}}`)
	}))
	defer srv.Close()

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	_, _, err := collect(p.Stream(context.Background(), Request{Model: "tiny"}))
	if !errors.Is(err, ErrQuota) {
		t.Fatalf("error = %v, want a quota error", err)
	}
}

func TestStreamAuthErrorRedactsEchoedKey(t *testing.T) {
	const key = "gsk_livekeyvalue0123456789abcdef"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		// Providers really do echo the offending credential back.
		_, _ = io.WriteString(w, `{"error":{"message":"Invalid API key: `+key+`"}}`)
	}))
	defer srv.Close()

	p := NewChatCompat("stub", srv.URL, key, WithHTTPClient(srv.Client()))
	_, _, err := collect(p.Stream(context.Background(), Request{Model: "tiny"}))
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("error = %v, want an auth error", err)
	}
	if strings.Contains(err.Error(), key) {
		t.Error("the API key survived into the error message")
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("error %q does not show the redaction marker", err.Error())
	}
}

func TestStreamOtherStatusIsPlainError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"malformed request"}}`)
	}))
	defer srv.Close()

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	_, _, err := collect(p.Stream(context.Background(), Request{Model: "tiny"}))
	if err == nil {
		t.Fatal("a 400 produced no error")
	}
	// Every provider would reject a bad request the same way, so a 400 carries
	// none of the sentinels that would send the session to the next provider.
	if errors.Is(err, ErrQuota) || errors.Is(err, ErrAuth) || errors.Is(err, ErrUnavailable) {
		t.Errorf("a 400 was classified as recoverable: %v", err)
	}
	if !strings.Contains(err.Error(), "malformed request") {
		t.Errorf("error %q does not relay the server message", err.Error())
	}
}

func TestStreamServerErrorIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"upstream exploded"}}`)
	}))
	defer srv.Close()

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	_, _, err := collect(p.Stream(context.Background(), Request{Model: "tiny"}))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want an unavailable error", err)
	}

	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("error %v is not an *UnavailableError", err)
	}
	if ue.Provider != "stub" {
		t.Errorf("provider = %q, want %q", ue.Provider, "stub")
	}
	if ue.Status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", ue.Status, http.StatusServiceUnavailable)
	}
	if ue.Reason != "upstream exploded" {
		t.Errorf("reason = %q, want %q", ue.Reason, "upstream exploded")
	}
	if !strings.Contains(err.Error(), "upstream exploded") {
		t.Errorf("error %q does not relay the server message", err.Error())
	}
}

func TestStreamServerErrorWithoutBodyStillSaysUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	_, _, err := collect(p.Stream(context.Background(), Request{Model: "tiny"}))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want an unavailable error", err)
	}
	// With no message to relay the status line stands in for one, so the error
	// still names what went wrong rather than trailing an empty reason.
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error %q does not mention the status code", err.Error())
	}
}

func TestStreamMalformedFrameIsReported(t *testing.T) {
	srv, _ := sseServer(t, []string{
		frame(`{"choices":[{"delta":{"content":"partial"}}]}`),
		frame(`{not json`),
	})

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	text, _, err := collect(p.Stream(context.Background(), Request{Model: "tiny"}))
	if err == nil {
		t.Fatal("a malformed frame produced no error")
	}
	if text != "partial" {
		t.Errorf("text before the failure = %q, want %q", text, "partial")
	}
}

func TestStreamHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	srv, _ := sseServer(t, []string{"data: [DONE]\n\n"})
	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	_, _, err := collect(p.Stream(ctx, Request{Model: "tiny"}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestModelsAcceptsEitherContextField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"data":[
			{"id":"a","context_window":8192},
			{"id":"b","context_length":32768},
			{"id":"c"}
		]}`)
	}))
	defer srv.Close()

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models failed: %v", err)
	}
	want := []Model{
		{ID: "a", Name: "a", ContextWindow: 8192},
		{ID: "b", Name: "b", ContextWindow: 32768},
		{ID: "c", Name: "c", ContextWindow: 0},
	}
	if len(models) != len(want) {
		t.Fatalf("got %d models, want %d", len(models), len(want))
	}
	for i := range want {
		if models[i] != want[i] {
			t.Errorf("model %d = %+v, want %+v", i, models[i], want[i])
		}
	}
}

// TestModelsCarriesPricingThroughToTiers walks a surplus-shaped catalogue from
// the wire to the tier the CLI bands it into, so a regression in the decimal
// string parsing shows up as a wrong tier rather than a silent zero rate.
func TestModelsCarriesPricingThroughToTiers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"data":[
			{"id":"openai-gpt-oss-120b","name":"GPT OSS 120B","context_length":128000,
			 "pricing":{"prompt":"0.0000000700","completion":"0.0000003000"}},
			{"id":"gratis","context_length":8192,
			 "pricing":{"prompt":"0","completion":"0"}},
			{"id":"frontier","context_length":200000,
			 "pricing":{"prompt":"0.0000020000","completion":"0.0000080000"}},
			{"id":"silent-rates","context_length":4096,
			 "pricing":{"prompt":"","completion":""}},
			{"id":"no-pricing-member","context_length":4096}
		]}`)
	}))
	defer srv.Close()

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models failed: %v", err)
	}
	want := []Model{
		{ID: "openai-gpt-oss-120b", Name: "GPT OSS 120B", ContextWindow: 128000,
			Pricing: Pricing{Known: true, Prompt: 7e-8, Completion: 3e-7}},
		{ID: "gratis", Name: "gratis", ContextWindow: 8192,
			Pricing: Pricing{Known: true}},
		{ID: "frontier", Name: "frontier", ContextWindow: 200000,
			Pricing: Pricing{Known: true, Prompt: 2e-6, Completion: 8e-6}},
		{ID: "silent-rates", Name: "silent-rates", ContextWindow: 4096},
		{ID: "no-pricing-member", Name: "no-pricing-member", ContextWindow: 4096},
	}
	if len(models) != len(want) {
		t.Fatalf("got %d models, want %d", len(models), len(want))
	}
	for i := range want {
		if models[i] != want[i] {
			t.Errorf("model %d = %+v, want %+v", i, models[i], want[i])
		}
	}

	tiers := []Tier{TierCheap, TierFree, TierPaid, TierUnknown, TierUnknown}
	for i, tier := range tiers {
		if got := models[i].Tier(); got != tier {
			t.Errorf("%s tier = %q, want %q", models[i].ID, got, tier)
		}
	}
}

func TestModelsPropagatesTypedErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	if _, err := p.Models(context.Background()); !errors.Is(err, ErrAuth) {
		t.Fatalf("error = %v, want an auth error", err)
	}
}

func TestRetryAfterParsesHTTPDate(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", time.Now().Add(90*time.Second).UTC().Format(http.TimeFormat))
	got := retryAfter(resp)
	// The header has one-second resolution, so allow a little slack.
	if got < 80*time.Second || got > 90*time.Second {
		t.Errorf("RetryAfter = %v, want roughly 90s", got)
	}
}

func TestRetryAfterAbsentOrUnparseable(t *testing.T) {
	for _, v := range []string{"", "soon", "-5"} {
		resp := &http.Response{Header: http.Header{}}
		if v != "" {
			resp.Header.Set("Retry-After", v)
		}
		if got := retryAfter(resp); got != 0 {
			t.Errorf("Retry-After %q gave %v, want 0", v, got)
		}
	}
}

// readSchema is a small but realistic parameter schema. It is written compactly
// so that a byte comparison after the round trip is meaningful: the adapter is
// supposed to pass the schema through untouched, not re-encode it.
const readSchema = `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`

func TestStreamSendsToolDefinitions(t *testing.T) {
	srv, seen := sseServer(t, []string{"data: [DONE]\n\n"})

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	_, _, err := collect(p.Stream(context.Background(), Request{
		Model:    "tiny",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools: []Tool{
			{Name: "read", Description: "Read a file", Parameters: json.RawMessage(readSchema)},
			{Name: "search"},
		},
	}))
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}

	if len(seen.Tools) != 2 {
		t.Fatalf("sent %d tools, want 2", len(seen.Tools))
	}
	read := seen.Tools[0]
	if read.Type != "function" {
		t.Errorf("tool type = %q, want function", read.Type)
	}
	if read.Function.Name != "read" || read.Function.Description != "Read a file" {
		t.Errorf("tool = %+v, want the read tool with its description", read.Function)
	}
	if got := string(read.Function.Parameters); got != readSchema {
		t.Errorf("schema = %s, want it passed through as %s", got, readSchema)
	}
	// A tool with no schema should not acquire an empty one on the way out.
	if got := seen.Tools[1].Function.Parameters; got != nil {
		t.Errorf("schemaless tool carried parameters %s, want none", got)
	}
}

// TestStreamOmitsToolsKeyWhenEmpty inspects the raw body rather than the decoded
// one, because the distinction that matters here — an absent key versus an empty
// array — does not survive decoding.
func TestStreamOmitsToolsKeyWhenEmpty(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	if _, _, err := collect(p.Stream(context.Background(), Request{
		Model:    "tiny",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})); err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if strings.Contains(string(body), "tools") {
		t.Errorf("request mentions tools with none to send: %s", body)
	}
}

func TestStreamReassemblesToolCallFragments(t *testing.T) {
	srv, _ := sseServer(t, []string{
		// Two calls, interleaved, with their arguments split across frames
		// and the second one introduced before the first is finished.
		frame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"read","arguments":"{\"path\":"}}]}}]}`),
		frame(`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"search","arguments":"{\"q\":\"todo\"}"}}]}}]}`),
		frame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"main.go\"}"}}]}}]}`),
		frame(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
		"data: [DONE]\n\n",
	})

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	text, chunks, err := collect(p.Stream(context.Background(), Request{
		Model:    "tiny",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}))
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if text != "" {
		t.Errorf("tool-only turn produced text %q, want none", text)
	}
	// Fragments are not chunks: nothing reaches the caller until the calls
	// are whole.
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1 carrying the finished calls", len(chunks))
	}
	got := chunks[0]
	if got.Finish != FinishTool {
		t.Errorf("finish reason = %q, want %q", got.Finish, FinishTool)
	}
	if len(got.ToolCalls) != 2 {
		t.Fatalf("got %d calls, want 2", len(got.ToolCalls))
	}
	first := got.ToolCalls[0]
	if first.ID != "call_a" || first.Name != "read" {
		t.Errorf("first call = %+v, want call_a/read", first)
	}
	if want := `{"path":"main.go"}`; first.Arguments != want {
		t.Errorf("first arguments = %q, want %q", first.Arguments, want)
	}
	// Ordering follows the server's numbering, not the arrival of the last
	// fragment, which here would have put the calls the other way around.
	if second := got.ToolCalls[1]; second.ID != "call_b" || second.Name != "search" {
		t.Errorf("second call = %+v, want call_b/search", second)
	}
}

// TestStreamReassemblesUnnumberedToolCalls covers servers that stream a single
// call and leave the index out entirely.
func TestStreamReassemblesUnnumberedToolCalls(t *testing.T) {
	srv, _ := sseServer(t, []string{
		frame(`{"choices":[{"delta":{"tool_calls":[{"id":"call_a","function":{"name":"read","arguments":"{\"path\""}}]}}]}`),
		frame(`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":":\"go.mod\"}"}}]}}]}`),
		frame(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
		"data: [DONE]\n\n",
	})

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	_, chunks, err := collect(p.Stream(context.Background(), Request{
		Model:    "tiny",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}))
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if len(chunks) != 1 || len(chunks[0].ToolCalls) != 1 {
		t.Fatalf("got %+v, want a single reassembled call", chunks)
	}
	call := chunks[0].ToolCalls[0]
	if want := `{"path":"go.mod"}`; call.ID != "call_a" || call.Arguments != want {
		t.Errorf("call = %+v, want call_a with arguments %q", call, want)
	}
}

// TestStreamFlushesToolCallsWithoutFinishReason covers the servers that go
// straight from the last argument fragment to the end of the stream. The calls
// are complete; only the announcement is missing.
func TestStreamFlushesToolCallsWithoutFinishReason(t *testing.T) {
	fragments := []string{
		frame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"read","arguments":"{}"}}]}}]}`),
	}
	cases := map[string][]string{
		"done sentinel": append(slices.Clone(fragments), "data: [DONE]\n\n"),
	}
	for name, frames := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _ := sseServer(t, frames)
			p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
			_, chunks, err := collect(p.Stream(context.Background(), Request{
				Model:    "tiny",
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			}))
			if err != nil {
				t.Fatalf("stream failed: %v", err)
			}
			if len(chunks) != 1 {
				t.Fatalf("got %d chunks, want the flushed call", len(chunks))
			}
			if chunks[0].Finish != FinishTool || len(chunks[0].ToolCalls) != 1 {
				t.Errorf("flushed chunk = %+v, want one call finished as %q", chunks[0], FinishTool)
			}
		})
	}
}

// TestStreamRestatesPlainStopAsTool covers a server that reports a tool turn as
// an ordinary stop. The turn is not over for the caller: there are calls to run.
func TestStreamRestatesPlainStopAsTool(t *testing.T) {
	srv, _ := sseServer(t, []string{
		frame(`{"choices":[{"delta":{"content":"Let me look."}}]}`),
		frame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"read","arguments":"{}"}}]},"finish_reason":"stop"}]}`),
		"data: [DONE]\n\n",
	})

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	text, chunks, err := collect(p.Stream(context.Background(), Request{
		Model:    "tiny",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}))
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if text != "Let me look." {
		t.Errorf("text = %q, want the prose that preceded the call", text)
	}
	last := chunks[len(chunks)-1]
	if last.Finish != FinishTool {
		t.Errorf("finish reason = %q, want it restated as %q", last.Finish, FinishTool)
	}
	if len(last.ToolCalls) != 1 {
		t.Fatalf("got %d calls, want 1", len(last.ToolCalls))
	}
}

// TestStreamReplaysToolMessages checks the other direction: an assistant turn
// that asked for a tool, and the result answering it, have to go back to the
// server in the shape it recognizes or the model loses the thread.
func TestStreamReplaysToolMessages(t *testing.T) {
	srv, seen := sseServer(t, []string{"data: [DONE]\n\n"})

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	_, _, err := collect(p.Stream(context.Background(), Request{
		Model: "tiny",
		Messages: []Message{
			{Role: RoleUser, Content: "what is in go.mod?"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{
				ID:        "call_a",
				Name:      "read",
				Arguments: `{"path":"go.mod"}`,
			}}},
			{Role: RoleTool, ToolCallID: "call_a", Content: "module yonderllm"},
		},
	}))
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}

	if len(seen.Messages) != 3 {
		t.Fatalf("sent %d messages, want 3", len(seen.Messages))
	}
	assistant := seen.Messages[1]
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant turn = %+v, want one tool call", assistant)
	}
	call := assistant.ToolCalls[0]
	if call.ID != "call_a" || call.Type != "function" {
		t.Errorf("replayed call = %+v, want call_a typed as a function", call)
	}
	if call.Function.Name != "read" || call.Function.Arguments != `{"path":"go.mod"}` {
		t.Errorf("replayed call function = %+v, want read with its arguments", call.Function)
	}
	result := seen.Messages[2]
	if result.Role != "tool" || result.ToolCallID != "call_a" {
		t.Errorf("result turn = %+v, want a tool message tied to call_a", result)
	}
	if result.Content != "module yonderllm" {
		t.Errorf("result content = %q, want the tool's output", result.Content)
	}
	// An ordinary turn should not pick up tool plumbing on the way out.
	if user := seen.Messages[0]; user.ToolCallID != "" || user.ToolCalls != nil {
		t.Errorf("user turn = %+v, want it free of tool fields", user)
	}
}

// TestFinishReasonVocabulary pins the whole wire vocabulary, including the
// spellings only some servers use and the deliberate fallback for values we
// have never seen.
func TestFinishReasonVocabulary(t *testing.T) {
	cases := []struct {
		wire string
		want FinishReason
	}{
		{"", FinishNone},
		{"stop", FinishStop},
		{"tool_calls", FinishTool},
		{"function_call", FinishTool},
		{"length", FinishLength},
		{"max_tokens", FinishLength},
		{"content_filter", FinishFilter},
		{"something_new", FinishStop},
	}
	for _, c := range cases {
		if got := finishReason(c.wire); got != c.want {
			t.Errorf("finishReason(%q) = %v, want %v", c.wire, got, c.want)
		}
	}
}

// TestBaseURLKeepsItsPathSegments pins the composition Gemini depends on: its
// base URL already carries a version path, so requests must land on
// <base>/chat/completions rather than at the server root. A trailing slash on
// the configured base must not double up on the way either.
func TestBaseURLKeepsItsPathSegments(t *testing.T) {
	const prefix = "/v1beta/openai"
	cases := []struct {
		name string
		base string
	}{
		{"bare", prefix},
		{"trailing slash", prefix + "/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				switch r.URL.Path {
				case prefix + "/chat/completions":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, frame(`{"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`))
					_, _ = io.WriteString(w, "data: [DONE]\n\n")
				case prefix + "/models":
					_, _ = io.WriteString(w, `{"data":[{"id":"gemini-2.0-flash","context_length":1048576}]}`)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			p := NewChatCompat("stub", srv.URL+c.base, "k", WithHTTPClient(srv.Client()))
			text, _, err := collect(p.Stream(context.Background(), Request{Model: "gemini-2.0-flash"}))
			if err != nil {
				t.Fatalf("Stream failed: %v", err)
			}
			if text != "hi" {
				t.Errorf("text = %q, want %q", text, "hi")
			}
			models, err := p.Models(context.Background())
			if err != nil {
				t.Fatalf("Models failed: %v", err)
			}
			if len(models) != 1 || models[0].ID != "gemini-2.0-flash" {
				t.Errorf("models = %+v, want the single catalogue entry", models)
			}
			want := []string{prefix + "/chat/completions", prefix + "/models"}
			if !slices.Equal(paths, want) {
				t.Errorf("requested paths = %v, want %v", paths, want)
			}
		})
	}
}

// TestEmptyModelIsAbsentFromTheBody guards the last line of defence. The
// session refuses to call a provider with no model configured, so an empty
// model reaching the adapter is a bug — but if one ever does, the key must be
// missing rather than sent as "", which reads to a server as a request for a
// model whose name is the empty string. The assertion works on the raw body
// because decoding into chatRequest cannot tell an absent key from an empty
// one.
func TestEmptyModelIsAbsentFromTheBody(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not valid JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, frame(`{"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`))
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	if _, _, err := collect(p.Stream(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})); err != nil {
		t.Fatalf("Stream failed: %v", err)
	}
	if _, ok := body["model"]; ok {
		t.Errorf("body carries model = %#v, want the key omitted", body["model"])
	}
	// A model that was set must still travel, or omitempty has gone too far.
	body = nil
	if _, _, err := collect(p.Stream(context.Background(), Request{
		Model:    "tiny",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})); err != nil {
		t.Fatalf("Stream failed: %v", err)
	}
	if got := body["model"]; got != "tiny" {
		t.Errorf("body model = %#v, want %q", got, "tiny")
	}
}
