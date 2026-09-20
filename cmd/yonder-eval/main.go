// Command yonder-eval runs offline fixtures by default. Live provider access
// requires explicit flags; reports contain only synthetic evaluation content.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/signal"
	"time"

	"yonderllm/internal/config"
	"yonderllm/internal/evaluation"
	"yonderllm/internal/provider"
	"yonderllm/internal/session"
)

type report struct {
	Schema         int                     `json:"schema"`
	Suite          string                  `json:"suite_version"`
	StartedAt      time.Time               `json:"started_at"`
	Mode           string                  `json:"mode"`
	MaxTokens      int                     `json:"max_tokens"`
	Timeout        string                  `json:"timeout"`
	Retries        int                     `json:"retries"`
	Fallbacks      []string                `json:"fallbacks"`
	Cancellation   evaluation.Cancellation `json:"cancellation"`
	Results        []evaluation.Result     `json:"results"`
	Passed         bool                    `json:"passed"`
	BudgetUSD      float64                 `json:"budget_usd,omitempty"`
	ReservedUSD    float64                 `json:"reserved_usd,omitempty"`
	PromptRate     float64                 `json:"prompt_usd_per_token,omitempty"`
	CompletionRate float64                 `json:"completion_usd_per_token,omitempty"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, out, stderr io.Writer) int {
	f := flag.NewFlagSet("yonder-eval", flag.ContinueOnError)
	f.SetOutput(stderr)
	live := f.Bool("live", false, "make billable provider requests (default: offline fixtures)")
	path := f.String("config", "", "explicit config path, required for live runs")
	name := f.String("provider", "", "provider to evaluate, required for live runs")
	model := f.String("model", "", "model to evaluate, required for live runs")
	timeout := f.Duration("timeout", 30*time.Second, "timeout per case")
	maxTokens := f.Int("max-tokens", 256, "output token limit per provider round")
	budgetUSD := f.Float64("budget-usd", 0, "optional conservative live token-cost cap; requires published model pricing")
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 || *timeout <= 0 || *timeout > time.Hour || *maxTokens <= 0 || *budgetUSD < 0 || math.IsNaN(*budgetUSD) || math.IsInf(*budgetUSD, 0) {
		fmt.Fprintln(stderr, "invalid arguments: positive timeout up to 1h and max-tokens required; no positional arguments")
		return 2
	}
	cfg := config.Config{Provider: "fixture", MaxTokens: *maxTokens, Providers: map[string]config.ProviderConfig{"fixture": {Model: "fixture-v1"}}}
	mode := "fixture"
	if *live {
		if *path == "" || *name == "" || *model == "" {
			fmt.Fprintln(stderr, "live runs require --config, --provider, and --model")
			return 2
		}
		var err error
		cfg, err = config.Load(*path)
		if err != nil {
			fmt.Fprintln(stderr, "cannot load evaluation config:", err)
			return 2
		}
		pc, exists := cfg.Providers[*name]
		if !exists || !cfg.Credentialed(*name) {
			fmt.Fprintln(stderr, "evaluation provider is missing or has no credential")
			return 2
		}
		pc.Model = *model
		cfg.Providers[*name] = pc
		cfg.Provider, cfg.MaxTokens = *name, *maxTokens
		// Each case has a fresh isolated session. No fallback, retries, saved
		// conversations, workspace access, or user's daily-counter mutation.
		cfg.Fallbacks, cfg.RetryAttempts, cfg.DailyCap, cfg.RequestTimeoutSeconds = nil, 0, 0, 0
		mode = "live"
	} else if *path != "" || *name != "" || *model != "" || *budgetUSD != 0 {
		fmt.Fprintln(stderr, "provider flags require --live")
		return 2
	}
	r := report{Schema: 1, Suite: evaluation.SuiteVersion, StartedAt: time.Now().UTC(), Mode: mode,
		MaxTokens: *maxTokens, Timeout: timeout.String(), Fallbacks: []string{}, Passed: true}
	var budget *evaluation.Budget
	if *budgetUSD > 0 {
		pc := cfg.Providers[cfg.Provider]
		priceCtx, cancel := context.WithTimeout(ctx, *timeout)
		catalogue, err := provider.NewChatCompat(cfg.Provider, pc.BaseURL, pc.APIKey()).Models(priceCtx)
		cancel()
		if err != nil {
			fmt.Fprintln(stderr, "cannot check model pricing:", err)
			return 2
		}
		for _, m := range catalogue {
			if m.ID == pc.Model && m.Pricing.Known && m.Pricing.Prompt >= 0 && m.Pricing.Completion >= 0 && !math.IsNaN(m.Pricing.Prompt+m.Pricing.Completion) && !math.IsInf(m.Pricing.Prompt+m.Pricing.Completion, 0) {
				budget = &evaluation.Budget{LimitUSD: *budgetUSD, PromptRate: m.Pricing.Prompt, CompletionRate: m.Pricing.Completion}
				break
			}
		}
		if budget == nil {
			fmt.Fprintln(stderr, "selected model has no valid published pricing; refusing budgeted run")
			return 2
		}
		r.BudgetUSD, r.PromptRate, r.CompletionRate = *budgetUSD, budget.PromptRate, budget.CompletionRate
	}
	var last provider.Provider
	for _, c := range evaluation.Cases() {
		var p provider.Provider = evaluation.Fixture(c.ID)
		if *live {
			pc := cfg.Providers[cfg.Provider]
			opts := []provider.ChatOption{}
			if budget != nil {
				opts = append(opts, provider.WithHTTPClient(&http.Client{Transport: budget}))
			}
			if pc.OmitStreamOptions {
				opts = append(opts, provider.WithoutStreamOptions())
			}
			for header, env := range pc.HeaderEnv {
				opts = append(opts, provider.WithHeader(header, os.Getenv(env)))
			}
			p = provider.NewChatCompat(cfg.Provider, pc.BaseURL, pc.APIKey(), opts...)
		}
		last = p
		s := session.New(cfg, func(string) (provider.Provider, error) { return p, nil })
		caseCtx, cancel := context.WithTimeout(ctx, *timeout)
		result := evaluation.Run(caseCtx, s, c, time.Now)
		cancel()
		r.Results = append(r.Results, result)
		r.Passed = r.Passed && result.Passed
		if ctx.Err() != nil || result.Error != "" {
			break
		}
	}
	if ctx.Err() == nil && len(r.Results) == len(evaluation.Cases()) && r.Results[len(r.Results)-1].Error == "" {
		probeCtx, cancel := context.WithTimeout(ctx, *timeout)
		if !*live {
			last = evaluation.Fixture("arithmetic")
		}
		s := session.New(cfg, func(string) (provider.Provider, error) { return last, nil })
		r.Cancellation = evaluation.ProbeCancellation(probeCtx, s, time.Now)
		cancel()
	} else {
		r.Cancellation.Error = "run interrupted or provider failed; cancellation probe skipped"
	}
	if budget != nil {
		r.ReservedUSD = budget.ReservedUSD()
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(r); err != nil {
		fmt.Fprintln(stderr, "write evaluation report:", err)
		return 2
	}
	if !r.Passed {
		return 1
	}
	return 0
}
