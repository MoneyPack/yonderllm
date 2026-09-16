package session

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"yonderllm/internal/provider"
)

func TestRetryDoesNotDuplicatePromptOrRunTools(t *testing.T) {
	p := &fakeProvider{name: "groq", chunks: textChunks("partial"), err: errors.New("connection lost")}
	s := New(testConfig("groq"), resolverFor(p))
	_, _, _, err := collect(s.Ask(context.Background(), "question"))
	if err == nil {
		t.Fatal("expected failure")
	}
	p.err = nil
	p.chunks = []provider.Chunk{{Delta: "answer", ToolCalls: []provider.ToolCall{{ID: "new", Name: "write", Arguments: `{}`}}}}
	runs := 0
	s.SetTools(Tool{Definition: provider.Tool{Name: "write"}, Run: func(context.Context, string) (string, error) { runs++; return "done", nil }})
	_, _, done, err := collect(s.Retry(context.Background()))
	if err != nil || !done {
		t.Fatalf("retry: done=%v error=%v", done, err)
	}
	if runs != 0 || len(p.lastReq.Tools) != 0 {
		t.Fatal("retry exposed or executed tools")
	}
	if len(p.lastReq.Messages) != 1 || p.lastReq.Messages[0].Content != "question" {
		t.Fatalf("retry prompt: %+v", p.lastReq.Messages)
	}
	if s.History().Len() != 2 {
		t.Fatal("duplicated prompt in history")
	}
	if err := s.CanRetry(); err == nil {
		t.Fatal("successful answer is retryable")
	}
}

func TestPartialFailureDoesNotMixFallbackAnswersOrRefund(t *testing.T) {
	p := &fakeProvider{name: "groq", chunks: textChunks("partial"), err: &provider.UnavailableError{Provider: "groq", Status: 503, Reason: "offline"}}
	other := &fakeProvider{name: "other", chunks: textChunks("different answer")}
	s := New(testConfig("groq", "other"), resolverFor(p, other))
	_, _, _, err := collect(s.Ask(context.Background(), "question"))
	if err == nil || other.calls != 0 {
		t.Fatalf("partial output triggered fallback: error=%v calls=%d", err, other.calls)
	}
	if s.Usage().Requests() != 1 {
		t.Fatal("partially answered request refunded")
	}
}

func TestRetryHonorsCapAndAutosaveFailureIsNotRetryable(t *testing.T) {
	p := &fakeProvider{name: "groq", chunks: textChunks("partial"), err: errors.New("offline")}
	cfg := testConfig("groq")
	cfg.DailyCap = 1
	s := New(cfg, resolverFor(p))
	collect(s.Ask(context.Background(), "question"))
	_, _, _, err := collect(s.Retry(context.Background()))
	var capErr *CapError
	if !errors.As(err, &capErr) || p.calls != 1 {
		t.Fatal("retry bypassed cap")
	}

	p.err = nil
	s = New(testConfig("groq"), resolverFor(p))
	// The store has disappeared after enabling saves.
	store := &Sessions{dir: filepath.Join(t.TempDir(), "missing")}
	if err := s.EnableSaving(store, "answer"); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = collect(s.Ask(context.Background(), "question"))
	if err == nil {
		t.Fatal("expected saving error")
	}
	if err := s.CanRetry(); err == nil {
		t.Fatal("completed answer with storage error can be repeated")
	}
}

func TestRetryAfterConsumerStopsUsesOriginalQuestion(t *testing.T) {
	p := &fakeProvider{name: "groq", chunks: textChunks("partial", "more")}
	s := New(testConfig("groq"), resolverFor(p))
	for range s.Ask(context.Background(), "question") {
		break
	}
	if err := s.CanRetry(); err != nil {
		t.Fatal(err)
	}
	p.chunks = textChunks("complete")
	_, _, done, err := collect(s.Retry(context.Background()))
	if err != nil || !done || len(p.lastReq.Messages) != 1 {
		t.Fatalf("retry after interruption: %v", err)
	}
}

func TestRetryPreservesCompletedToolResults(t *testing.T) {
	p := &fakeProvider{name: "groq", chunks: []provider.Chunk{{ToolCalls: []provider.ToolCall{{ID: "call", Name: "write", Arguments: `{}`}}}}}
	s := New(testConfig("groq"), resolverFor(p))
	runs := 0
	s.SetTools(Tool{Definition: provider.Tool{Name: "write"}, Run: func(context.Context, string) (string, error) {
		runs++
		p.chunks = nil
		p.err = errors.New("upstream dropped")
		return "already written", nil
	}})
	_, _, _, err := collect(s.Ask(context.Background(), "write then explain"))
	if err == nil {
		t.Fatal("expected failed follow-up")
	}
	p.err = nil
	p.chunks = textChunks("explanation")
	_, _, _, err = collect(s.Retry(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("write executed %d times", runs)
	}
	if len(p.lastReq.Messages) != 3 || p.lastReq.Messages[2].Content != "already written" {
		t.Fatalf("lost tool result: %+v", p.lastReq.Messages)
	}
}

func TestRetryRefusesFreshClearedAndIncompleteHistory(t *testing.T) {
	p := &fakeProvider{name: "groq", err: errors.New("offline")}
	s := New(testConfig("groq"), resolverFor(p))
	if err := s.CanRetry(); err == nil {
		t.Fatal("fresh session retryable")
	}
	collect(s.Ask(context.Background(), "hello"))
	s.history.AppendToolCalls("", []provider.ToolCall{{ID: "unfinished", Name: "write"}})
	if err := s.CanRetry(); err == nil {
		t.Fatal("incomplete tool exchange retryable")
	}
	s.Clear()
	if err := s.CanRetry(); err == nil {
		t.Fatal("clear retained retry state")
	}
}
