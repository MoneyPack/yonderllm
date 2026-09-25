package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MoneyPack/yonderllm/internal/config"
	"github.com/MoneyPack/yonderllm/internal/provider"
)

// Every event after a fallback must name the fallback's model, not the one the
// session was started with: a consumer that stamps events with Session.Model
// would otherwise attribute the answer to a model that never produced it.
func TestEventsAfterFallbackCarryTheFallbackModel(t *testing.T) {
	first := &fakeProvider{name: "groq", err: &provider.QuotaError{Provider: "groq"}}
	second := &fakeProvider{name: "gemini", chunks: append(textChunks("ok"), provider.Chunk{Finish: provider.FinishStop})}
	s := New(testConfig("groq", "gemini"), resolverFor(first, second))

	var events []Event
	for ev, err := range s.Ask(context.Background(), "hi") {
		if err != nil {
			t.Fatalf("Ask returned error: %v", err)
		}
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatal("no events")
	}
	for _, ev := range events {
		if ev.Provider != "gemini" {
			t.Errorf("event %+v names provider %q, want gemini", ev, ev.Provider)
		}
		if ev.Model != "gemini-model" {
			t.Errorf("event %+v carries model %q, want gemini-model", ev, ev.Model)
		}
	}
	// The session's own view is unchanged: the user still prefers groq.
	if got := s.Model(); got != "groq-model" {
		t.Errorf("Session.Model() = %q after a fallback, want groq-model", got)
	}
}

// A model chosen with /model belongs to the session. The configuration it was
// built from is shared with the rest of the program and must not change.
func TestSetModelDoesNotMutateTheConfig(t *testing.T) {
	cfg := testConfig("groq")
	s := New(cfg, resolverFor(&fakeProvider{name: "groq"}))

	s.SetModel("override")
	s.SetContextWindow("groq", 4096)

	if got := cfg.Providers["groq"].Model; got != "groq-model" {
		t.Errorf("config model = %q after SetModel, want groq-model", got)
	}
	if got := cfg.Providers["groq"].ContextWindow; got != 0 {
		t.Errorf("config context window = %d after SetContextWindow, want 0", got)
	}
	if got := s.Model(); got != "override" {
		t.Errorf("Session.Model() = %q, want override", got)
	}
}

// Options handed to NewWithOptions are copied: the caller editing its own
// value afterwards is not editing the session.
func TestNewWithOptionsCopiesProviders(t *testing.T) {
	opts := Options{
		Provider:  "groq",
		MaxTokens: 100,
		Providers: map[string]ProviderOptions{"groq": {Model: "a", Credentialed: true}},
	}
	s := NewWithOptions(opts, resolverFor())
	opts.Providers["groq"] = ProviderOptions{Model: "b", Credentialed: true}
	opts.Providers["extra"] = ProviderOptions{Model: "c"}

	if got := s.Model(); got != "a" {
		t.Errorf("Model() = %q, want a", got)
	}
	if got := s.Providers(); len(got) != 1 || got[0] != "groq" {
		t.Errorf("Providers() = %v, want [groq]", got)
	}
}

// A context_window in the config reaches trimming without anyone calling
// SetContextWindow: the budget is the window less the output reserve.
func TestConfiguredContextWindowDrivesTrimming(t *testing.T) {
	cfg := testConfig("groq")
	cfg.MaxTokens = 100
	pc := cfg.Providers["groq"]
	pc.ContextWindow = 400
	cfg.Providers["groq"] = pc

	p := &fakeProvider{name: "groq", chunks: textChunks("ok")}
	s := New(cfg, resolverFor(p))
	if got, want := s.promptBudget("groq"), 300; got != want {
		t.Fatalf("promptBudget = %d, want %d", got, want)
	}

	// Four turns of roughly 100 tokens each cannot all fit in 300, so the
	// oldest must be dropped from what the provider is sent.
	big := strings.Repeat("word ", 80)
	for range 3 {
		s.history.Append(provider.RoleUser, big)
		s.history.Append(provider.RoleAssistant, big)
	}
	if _, _, _, err := collect(s.Ask(context.Background(), big)); err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if sent := len(p.lastReq.Messages); sent >= 7 {
		t.Errorf("provider was sent %d messages, want the configured window to trim some", sent)
	}
	if TotalTokens(p.lastReq.Messages) > 300 {
		t.Errorf("provider was sent %d tokens, want at most the 300 budget", TotalTokens(p.lastReq.Messages))
	}

	// Without a configured window the same history fits the default.
	wide := New(testConfig("groq"), resolverFor(p))
	for range 3 {
		wide.history.Append(provider.RoleUser, big)
		wide.history.Append(provider.RoleAssistant, big)
	}
	if _, _, _, err := collect(wide.Ask(context.Background(), big)); err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if sent := len(p.lastReq.Messages); sent != 7 {
		t.Errorf("provider was sent %d messages with the default window, want all 7", sent)
	}
}

// OptionsFromConfig reduces a credential to yes/no and carries the window, so
// the session can be built without ever holding the key.
func TestOptionsFromConfig(t *testing.T) {
	cfg := testConfig("groq", "gemini")
	cfg.Providers["groq"] = config.ProviderConfig{Model: "groq-model", APIKeyEnv: "MISSING_KEY_ENV", ContextWindow: 8000}
	cfg.RequestTimeoutSeconds = 7

	opts := OptionsFromConfig(cfg)
	if opts.Provider != "groq" || len(opts.Fallbacks) != 1 || opts.Fallbacks[0] != "gemini" {
		t.Errorf("chain = %q + %v, want groq + [gemini]", opts.Provider, opts.Fallbacks)
	}
	if opts.RequestTimeoutSeconds != 7 || opts.MaxTokens != config.DefaultMaxTokens {
		t.Errorf("settings not carried: %+v", opts)
	}
	if got := opts.Providers["groq"]; got.Credentialed || got.ContextWindow != 8000 || got.Model != "groq-model" {
		t.Errorf("groq options = %+v, want uncredentialed with window 8000", got)
	}
	if got := opts.Providers["gemini"]; !got.Credentialed {
		t.Errorf("gemini options = %+v, want credentialed (no key required)", got)
	}
}

// The bare "context deadline exceeded" a timeout produces says nothing about
// which setting expired. When the session's own timeout fires the message
// names it, and the original error stays matchable.
func TestExchangeTimeoutNamesTheSetting(t *testing.T) {
	p := &deadlineProvider{fakeProvider: fakeProvider{name: "groq"}}
	cfg := testConfig("groq")
	cfg.RequestTimeoutSeconds = 1
	s := New(cfg, func(string) (provider.Provider, error) { return p, nil })

	_, _, _, err := collect(s.Ask(context.Background(), "hello"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if !strings.Contains(err.Error(), "request_timeout_seconds") {
		t.Errorf("err = %q, want it to name request_timeout_seconds", err)
	}
}

// A deadline the caller imposed is the caller's business; blaming the config
// setting for it would send the user to the wrong knob.
func TestCallerDeadlineIsNotBlamedOnTheSetting(t *testing.T) {
	p := &deadlineProvider{fakeProvider: fakeProvider{name: "groq"}}
	cfg := testConfig("groq")
	cfg.RequestTimeoutSeconds = 30
	s := New(cfg, func(string) (provider.Provider, error) { return p, nil })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, _, err := collect(s.Ask(ctx, "hello"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if strings.Contains(err.Error(), "request_timeout_seconds") {
		t.Errorf("err = %q names request_timeout_seconds for a caller-imposed deadline", err)
	}
}
