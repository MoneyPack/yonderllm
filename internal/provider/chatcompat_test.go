package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
			io.WriteString(w, f)
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
		io.WriteString(w, "data: [DONE]\n\n")
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
		io.WriteString(w, `{"error":{"message":"rate limit reached","type":"rate_limit"}}`)
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
		io.WriteString(w, `{"error":{"message":"credits exhausted"}}`)
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
		io.WriteString(w, `{"error":{"message":"Invalid API key: `+key+`"}}`)
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
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"message":"upstream exploded"}}`)
	}))
	defer srv.Close()

	p := NewChatCompat("stub", srv.URL, "k", WithHTTPClient(srv.Client()))
	_, _, err := collect(p.Stream(context.Background(), Request{Model: "tiny"}))
	if err == nil {
		t.Fatal("a 500 produced no error")
	}
	if errors.Is(err, ErrQuota) || errors.Is(err, ErrAuth) {
		t.Errorf("a 500 was classified as quota or auth: %v", err)
	}
	if !strings.Contains(err.Error(), "upstream exploded") {
		t.Errorf("error %q does not relay the server message", err.Error())
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
		io.WriteString(w, `{"data":[
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
		io.WriteString(w, `{"data":[
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
		io.WriteString(w, `{"error":{"message":"bad key"}}`)
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

func TestLooksLikeKey(t *testing.T) {
	keys := []string{
		"sk-abcdef",
		"sk_abcdef",
		"gsk_abcdef",
		"AIzaSyAbCdEf",
		"or-v1-abc",
		"abcdefghijklmnopqrstuvwxyz012345", // long and opaque
	}
	for _, k := range keys {
		if !looksLikeKey(k) {
			t.Errorf("looksLikeKey(%q) = false, want true", k)
		}
	}

	notKeys := []string{
		"hello",
		"rate limit reached",
		"short-token",
		"a sentence that is long enough overall", // spaces disqualify
	}
	for _, s := range notKeys {
		if looksLikeKey(s) {
			t.Errorf("looksLikeKey(%q) = true, want false", s)
		}
	}
}

func TestRedactKeyishKeepsSurroundingText(t *testing.T) {
	got := redactKeyish(`Invalid API key: "gsk_abcdefghijklmnop", try again.`)
	if strings.Contains(got, "gsk_") {
		t.Errorf("redaction left the key behind: %q", got)
	}
	if !strings.HasPrefix(got, "Invalid API key:") {
		t.Errorf("redaction damaged the message: %q", got)
	}
	if !strings.Contains(got, "try again.") {
		t.Errorf("redaction dropped trailing text: %q", got)
	}
}
