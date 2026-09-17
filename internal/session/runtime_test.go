package session

import (
	"context"
	"testing"
	"yonderllm/internal/provider"
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
