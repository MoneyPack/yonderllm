package evaluation

import (
	"context"
	"testing"
	"time"

	"yonderllm/internal/config"
	"yonderllm/internal/provider"
	"yonderllm/internal/session"
)

func TestFixtureSuite(t *testing.T) {
	for _, c := range Cases() {
		t.Run(c.ID, func(t *testing.T) {
			cfg := config.Config{Provider: "fixture", MaxTokens: 256, Providers: map[string]config.ProviderConfig{"fixture": {Model: "fixture-v1"}}}
			s := session.New(cfg, func(string) (provider.Provider, error) { return Fixture(c.ID), nil })
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
	cfg := config.Config{Provider: "fixture", MaxTokens: 256, Providers: map[string]config.ProviderConfig{"fixture": {Model: "fixture-v1"}}}
	s := session.New(cfg, func(string) (provider.Provider, error) { return Fixture(c.ID), nil })
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
	cfg := config.Config{Provider: "fixture", MaxTokens: 256, Providers: map[string]config.ProviderConfig{"fixture": {Model: "fixture-v1"}}}
	s := session.New(cfg, func(string) (provider.Provider, error) { return Fixture("arithmetic"), nil })
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
