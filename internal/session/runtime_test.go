package session

import (
	"context"
	"errors"
	"github.com/MoneyPack/yonderllm/internal/provider"
	"iter"
	"testing"
	"time"
)

func TestConfiguredRetriesAreBoundedAndNeverRepeatPartialText(t *testing.T) {
	for _, partial := range []bool{false, true} {
		p := &fakeProvider{name: "groq", err: provider.ErrUnavailable}
		if partial {
			p.chunks = textChunks("partial")
		}
		cfg := testConfig("groq")
		cfg.RetryAttempts = 2
		s := New(cfg, resolverFor(p))
		_, _, _, err := collect(s.Ask(context.Background(), "hello"))
		want := 3
		if partial {
			want = 1
		}
		if err == nil || p.calls != want {
			t.Fatalf("partial=%v calls=%d want=%d err=%v", partial, p.calls, want, err)
		}
	}
}

type deadlineProvider struct{ fakeProvider }

func (p *deadlineProvider) Stream(ctx context.Context, _ provider.Request) iter.Seq2[provider.Chunk, error] {
	return func(yield func(provider.Chunk, error) bool) {
		if _, ok := ctx.Deadline(); !ok {
			yield(provider.Chunk{}, errors.New("missing exchange deadline"))
			return
		}
		<-ctx.Done()
		yield(provider.Chunk{}, ctx.Err())
	}
}

func TestExchangeDeadlineStopsWaitingProvider(t *testing.T) {
	p := &deadlineProvider{fakeProvider: fakeProvider{name: "groq"}}
	cfg := testConfig("groq")
	cfg.RequestTimeoutSeconds = 1
	s := New(cfg, func(string) (provider.Provider, error) { return p, nil })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, _, done, err := collect(s.Ask(ctx, "hello"))
	if done || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2500*time.Millisecond {
		t.Fatalf("deadline: done=%v err=%v duration=%v", done, err, time.Since(start))
	}
}

func TestCancelDuringBackoffDoesNotDispatchAnotherAttempt(t *testing.T) {
	p := &fakeProvider{name: "groq", err: provider.ErrUnavailable}
	cfg := testConfig("groq")
	cfg.RetryAttempts = 3
	cfg.RetryBackoffMS = 30000
	s := New(cfg, resolverFor(p))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got error
	for event, err := range s.Ask(ctx, "hello") {
		if event.Notice != "" {
			cancel()
		}
		if err != nil {
			got = err
		}
	}
	if !errors.Is(got, context.Canceled) || p.calls != 1 {
		t.Fatalf("calls=%d error=%v", p.calls, got)
	}
}

func TestCanceledRetryContextIsCheckedAfterBackoff(t *testing.T) {
	p := &fakeProvider{name: "groq", err: provider.ErrUnavailable}
	cfg := testConfig("groq")
	cfg.RetryAttempts = 1
	cfg.RetryBackoffMS = 1
	s := New(cfg, resolverFor(p))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for event, err := range s.Ask(ctx, "hello") {
		if event.Notice != "" {
			cancel()
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if p.calls != 1 {
		t.Fatalf("canceled retry dispatched %d attempts", p.calls)
	}
}
