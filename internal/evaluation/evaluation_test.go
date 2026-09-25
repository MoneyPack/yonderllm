package evaluation

import (
	"context"
	"iter"
	"testing"
	"time"

	"github.com/MoneyPack/yonderllm/internal/provider"
	"github.com/MoneyPack/yonderllm/internal/session"
)

// fixtureOptions is the session every offline case runs under: one always
// credentialed provider, no fallbacks, no cap. No config file is involved.
func fixtureOptions() session.Options {
	return session.Options{
		Provider:  "fixture",
		MaxTokens: 256,
		Providers: map[string]session.ProviderOptions{"fixture": {Model: "fixture-v1", Credentialed: true}},
	}
}

func TestFixtureSuite(t *testing.T) {
	for _, c := range Cases() {
		t.Run(c.ID, func(t *testing.T) {
			opts := fixtureOptions()
			s := session.NewWithOptions(opts, func(string) (provider.Provider, error) { return Fixture(c.ID), nil })
			report := Run(context.Background(), s, c, time.Now)
			if !report.Passed {
				t.Fatalf("fixture failed: %+v", report)
			}
			if report.FirstTokenMS == nil || report.TotalMS < *report.FirstTokenMS {
				t.Fatalf("invalid latency measurements: %+v", report)
			}
		})
	}
}

type scriptedProvider struct {
	chunks []provider.Chunk
	index  int
}

func (p *scriptedProvider) Name() string                                     { return "fixture" }
func (p *scriptedProvider) Models(context.Context) ([]provider.Model, error) { return nil, nil }
func (p *scriptedProvider) Stream(context.Context, provider.Request) iter.Seq2[provider.Chunk, error] {
	return func(yield func(provider.Chunk, error) bool) {
		chunk := p.chunks[p.index]
		p.index++
		yield(chunk, nil)
	}
}
func TestFinalAnswerAndIgnoredToolCallsAreReportedHonestly(t *testing.T) {
	c := Cases()[2]
	call := provider.ToolCall{ID: "call", Name: "lookup", Arguments: `{"key":"color"}`}
	for _, test := range []struct {
		name   string
		chunks []provider.Chunk
		calls  int
		answer string
	}{
		{"split answer", []provider.Chunk{{Delta: "am", Finish: provider.FinishTool, ToolCalls: []provider.ToolCall{call}}, {Delta: "ber", Finish: provider.FinishStop}}, 1, "ber"},
		{"ignored sixth call", []provider.Chunk{
			{Finish: provider.FinishTool, ToolCalls: []provider.ToolCall{call}}, {Finish: provider.FinishTool, ToolCalls: []provider.ToolCall{call}},
			{Finish: provider.FinishTool, ToolCalls: []provider.ToolCall{call}}, {Finish: provider.FinishTool, ToolCalls: []provider.ToolCall{call}},
			{Finish: provider.FinishTool, ToolCalls: []provider.ToolCall{call}}, {Finish: provider.FinishTool, ToolCalls: []provider.ToolCall{call}},
		}, 6, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := &scriptedProvider{chunks: test.chunks}
			opts := fixtureOptions()
			s := session.NewWithOptions(opts, func(string) (provider.Provider, error) { return p, nil })
			r := Run(context.Background(), s, c, time.Now)
			if r.Passed || r.Answer != test.answer || len(r.ToolCalls) != test.calls {
				t.Fatalf("misleading report: %+v", r)
			}
		})
	}
}

func TestJudgeRejectsWrongAnswersAndExtraTools(t *testing.T) {
	c := Cases()[0]
	for _, r := range []Result{
		{Answer: "41", Completed: true},
		{Answer: "42", Completed: false},
		{Answer: "42", Completed: true, ToolCalls: []Call{{Name: "invented"}}},
		{Answer: "42", Completed: true, Error: "stream interrupted"},
	} {
		judge(&r, c)
		if r.Passed {
			t.Fatalf("false-positive pass: %+v", r)
		}
	}
}

func TestLatencyClockAndCancellation(t *testing.T) {
	c := Cases()[0]
	opts := fixtureOptions()
	s := session.NewWithOptions(opts, func(string) (provider.Provider, error) { return Fixture(c.ID), nil })
	n := 0
	now := func() time.Time { v := time.Unix(0, int64(n)*int64(10*time.Millisecond)); n++; return v }
	r := Run(context.Background(), s, c, now)
	if r.FirstTokenMS == nil || *r.FirstTokenMS != 10 || r.TotalMS != 20 {
		t.Fatalf("clock accounting: %+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = Run(ctx, s, c, time.Now)
	if r.Passed || r.Completed || r.FirstTokenMS != nil || r.Error == "" {
		t.Fatalf("cancelled request reported success: %+v", r)
	}
}

func TestProbeCancellationRecordsUnwindAndMissingSample(t *testing.T) {
	opts := fixtureOptions()
	s := session.NewWithOptions(opts, func(string) (provider.Provider, error) { return Fixture("arithmetic"), nil })
	n := 0
	now := func() time.Time { n++; return time.Unix(0, int64(n)*int64(time.Millisecond)) }
	r := ProbeCancellation(context.Background(), s, now)
	if !r.Observed || r.LatencyMS == nil || *r.LatencyMS != 1 {
		t.Fatalf("unwind not measured: %+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = ProbeCancellation(ctx, s, now)
	if r.Observed || r.LatencyMS != nil || r.Error == "" {
		t.Fatalf("missing sample treated as zero: %+v", r)
	}
}
