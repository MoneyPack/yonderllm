package session

import (
	"context"
	"strings"
	"testing"

	"github.com/MoneyPack/yonderllm/internal/provider"
)

func TestOutputLimitPreservesAnswerAndExplainsCutoff(t *testing.T) {
	p := &fakeProvider{name: "groq", chunks: []provider.Chunk{
		{Delta: "partial answer", Finish: provider.FinishLength},
		{Usage: &provider.Usage{CompletionTokens: 8}},
	}}
	s := New(testConfig("groq"), resolverFor(p))
	deltas, notices, done, err := collect(s.Ask(context.Background(), "hello"))
	if err != nil || !done || strings.Join(deltas, "") != "partial answer" {
		t.Fatalf("deltas=%v done=%v error=%v", deltas, done, err)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "output limit") || !strings.Contains(notices[0], "--max-tokens") {
		t.Fatalf("missing cutoff guidance: %v", notices)
	}
	if s.History().Turns()[1].Content != "partial answer" || s.CanRetry() == nil {
		t.Fatal("completed capped answer must be retained without enabling failed-exchange retry")
	}
}

func TestNormalStopDoesNotWarnAboutOutputLimit(t *testing.T) {
	p := &fakeProvider{name: "groq", chunks: []provider.Chunk{{Delta: "complete", Finish: provider.FinishStop}}}
	s := New(testConfig("groq"), resolverFor(p))
	_, notices, done, err := collect(s.Ask(context.Background(), "hello"))
	if err != nil || !done || len(notices) != 0 {
		t.Fatalf("done=%v notices=%v error=%v", done, notices, err)
	}
}
