package session

import (
	"context"
	"errors"
	"iter"
	"slices"
	"strings"
	"testing"

	"yonderllm/internal/config"
	"yonderllm/internal/provider"
)

// fakeProvider is a scripted adapter. It records what it was asked so tests can
// assert on the trimmed prompt the session actually built, and yields a fixed
// chunk sequence followed by an optional terminal error.
type fakeProvider struct {
	name    string
	chunks  []provider.Chunk
	err     error
	calls   int
	lastReq provider.Request
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Models(ctx context.Context) ([]provider.Model, error) {
	return []provider.Model{{ID: "fake-model", Name: "Fake", Free: true}}, nil
}

func (f *fakeProvider) Stream(ctx context.Context, req provider.Request) iter.Seq2[provider.Chunk, error] {
	f.calls++
	f.lastReq = req
	return func(yield func(provider.Chunk, error) bool) {
		for _, c := range f.chunks {
			if !yield(c, nil) {
				return
			}
		}
		if f.err != nil {
			yield(provider.Chunk{}, f.err)
		}
	}
}

// textChunks turns plain strings into delta-only chunks.
func textChunks(parts ...string) []provider.Chunk {
	out := make([]provider.Chunk, 0, len(parts))
	for _, p := range parts {
		out = append(out, provider.Chunk{Delta: p})
	}
	return out
}

// resolverFor maps provider names onto fakes and fails on anything unknown, so
// a test that mis-names a provider gets a clear error rather than a nil panic.
func resolverFor(fakes ...*fakeProvider) Resolver {
	byName := make(map[string]*fakeProvider, len(fakes))
	for _, f := range fakes {
		byName[f.name] = f
	}
	return func(name string) (provider.Provider, error) {
		f, ok := byName[name]
		if !ok {
			return nil, errors.New("no such provider: " + name)
		}
		return f, nil
	}
}

// testConfig builds a config whose providers are all credentialed. A provider
// with no APIKeyEnv counts as credentialed, which is the only way a test can
// simulate a usable key: the resolved key field is unexported and set at load.
func testConfig(active string, fallbacks ...string) config.Config {
	providers := map[string]config.ProviderConfig{
		active: {Model: active + "-model"},
	}
	for _, f := range fallbacks {
		providers[f] = config.ProviderConfig{Model: f + "-model"}
	}
	return config.Config{
		Provider:  active,
		Fallbacks: fallbacks,
		Mode:      config.DefaultMode,
		MaxTokens: config.DefaultMaxTokens,
		DailyCap:  config.DefaultDailyCap,
		Providers: providers,
	}
}

// collect drains an exchange into its deltas, notices and terminal error.
func collect(seq func(func(Event, error) bool)) (deltas []string, notices []string, done bool, err error) {
	for ev, e := range seq {
		if e != nil {
			err = e
			return
		}
		if ev.Delta != "" {
			deltas = append(deltas, ev.Delta)
		}
		if ev.Notice != "" {
			notices = append(notices, ev.Notice)
		}
		if ev.Done {
			done = true
		}
	}
	return
}

func TestAskStreamsAndRecordsHistory(t *testing.T) {
	p := &fakeProvider{
		name:   "groq",
		chunks: append(textChunks("Hel", "lo"), provider.Chunk{Finish: provider.FinishStop, Usage: &provider.Usage{PromptTokens: 11, CompletionTokens: 2}}),
	}
	s := New(testConfig("groq"), resolverFor(p))

	deltas, notices, done, err := collect(s.Ask(context.Background(), "hi"))
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if got := strings.Join(deltas, ""); got != "Hello" {
		t.Errorf("reply = %q, want %q", got, "Hello")
	}
	if len(notices) != 0 {
		t.Errorf("unexpected notices: %v", notices)
	}
	if !done {
		t.Error("no Done event was emitted")
	}

	turns := s.History().Turns()
	if len(turns) != 2 {
		t.Fatalf("history has %d turns, want 2", len(turns))
	}
	if turns[0].Role != provider.RoleUser || turns[0].Content != "hi" {
		t.Errorf("user turn = %+v", turns[0])
	}
	if turns[1].Role != provider.RoleAssistant || turns[1].Content != "Hello" {
		t.Errorf("assistant turn = %+v", turns[1])
	}

	totals, ok := s.Usage().ByProvider()["groq"]
	if !ok {
		t.Fatal("no usage recorded for groq")
	}
	if totals.PromptTokens != 11 || totals.CompletionTokens != 2 {
		t.Errorf("usage = %+v, want prompt 11 completion 2", totals)
	}
}

func TestAskFallsBackOnQuota(t *testing.T) {
	first := &fakeProvider{name: "groq", err: &provider.QuotaError{Provider: "groq"}}
	second := &fakeProvider{name: "gemini", chunks: textChunks("ok")}
	s := New(testConfig("groq", "gemini"), resolverFor(first, second))

	deltas, notices, done, err := collect(s.Ask(context.Background(), "hi"))
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if !done {
		t.Error("no Done event was emitted")
	}
	if got := strings.Join(deltas, ""); got != "ok" {
		t.Errorf("reply = %q, want %q", got, "ok")
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "gemini") {
		t.Errorf("notices = %v, want one mentioning gemini", notices)
	}
	if first.calls != 1 || second.calls != 1 {
		t.Errorf("calls: groq=%d gemini=%d, want 1 and 1", first.calls, second.calls)
	}
	// A fallback is part of the same exchange and must not cost a second
	// reservation against the daily cap.
	if got := s.Usage().Requests(); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

func TestAskFallsBackOnAuth(t *testing.T) {
	first := &fakeProvider{name: "groq", err: &provider.AuthError{Provider: "groq", Reason: "bad key"}}
	second := &fakeProvider{name: "gemini", chunks: textChunks("ok")}
	s := New(testConfig("groq", "gemini"), resolverFor(first, second))

	if _, _, done, err := collect(s.Ask(context.Background(), "hi")); err != nil || !done {
		t.Fatalf("Ask: done=%v err=%v, want done with no error", done, err)
	}
	if second.calls != 1 {
		t.Errorf("fallback provider called %d times, want 1", second.calls)
	}
}

func TestAskAbortsOnNonRecoverableError(t *testing.T) {
	boom := errors.New("connection reset")
	first := &fakeProvider{name: "groq", err: boom}
	second := &fakeProvider{name: "gemini", chunks: textChunks("ok")}
	s := New(testConfig("groq", "gemini"), resolverFor(first, second))

	_, _, done, err := collect(s.Ask(context.Background(), "hi"))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if done {
		t.Error("Done was emitted for a failed exchange")
	}
	if second.calls != 0 {
		t.Errorf("fallback was tried %d times, want 0: a transport failure is not recoverable by switching provider", second.calls)
	}
	// The failed request is refunded, and only the user turn survives.
	if got := s.Usage().Requests(); got != 0 {
		t.Errorf("requests = %d, want 0 after refund", got)
	}
	if got := s.History().Len(); got != 1 {
		t.Errorf("history has %d turns, want 1 (user only)", got)
	}
}

func TestAskAllProvidersExhausted(t *testing.T) {
	first := &fakeProvider{name: "groq", err: &provider.QuotaError{Provider: "groq"}}
	second := &fakeProvider{name: "gemini", err: &provider.QuotaError{Provider: "gemini"}}
	s := New(testConfig("groq", "gemini"), resolverFor(first, second))

	if _, _, _, err := collect(s.Ask(context.Background(), "hi")); err == nil {
		t.Fatal("Ask succeeded, want an error once every provider is exhausted")
	} else if !strings.Contains(err.Error(), "all providers failed") {
		t.Errorf("err = %v, want it to mention that all providers failed", err)
	}
	if got := s.Usage().Requests(); got != 0 {
		t.Errorf("requests = %d, want 0 after refund", got)
	}
	if got := s.History().Len(); got != 1 {
		t.Errorf("history has %d turns, want 1 (user only)", got)
	}
}

func TestAskStopsWhenConsumerBreaks(t *testing.T) {
	p := &fakeProvider{name: "groq", chunks: textChunks("one", "two", "three")}
	s := New(testConfig("groq"), resolverFor(p))

	var seen int
	for range s.Ask(context.Background(), "hi") {
		seen++
		break
	}
	if seen != 1 {
		t.Errorf("received %d events after break, want 1", seen)
	}
	// The assistant turn is never appended, so an abandoned reply cannot be
	// resent as context on the next question.
	if got := s.History().Len(); got != 1 {
		t.Errorf("history has %d turns, want 1 (user only)", got)
	}
	// A consumer that walks away still spent the request: the provider was
	// called and the tokens are gone, so the reservation is not refunded.
	if got := s.Usage().Requests(); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

func TestAskRefusesWhenDailyCapReached(t *testing.T) {
	p := &fakeProvider{name: "groq", chunks: textChunks("ok")}
	cfg := testConfig("groq")
	cfg.DailyCap = 1
	s := New(cfg, resolverFor(p))

	if err := s.Usage().Reserve(); err != nil {
		t.Fatalf("first Reserve: %v", err)
	}

	_, _, _, err := collect(s.Ask(context.Background(), "hi"))
	var capErr *CapError
	if !errors.As(err, &capErr) {
		t.Fatalf("err = %v, want a *CapError", err)
	}
	if p.calls != 0 {
		t.Errorf("provider called %d times, want 0 once the cap is reached", p.calls)
	}
	if got := s.History().Len(); got != 0 {
		t.Errorf("history has %d turns, want 0: a refused request is not a turn", got)
	}
}

func TestAskSkipsUncredentialedProviders(t *testing.T) {
	first := &fakeProvider{name: "groq"}
	second := &fakeProvider{name: "gemini", chunks: textChunks("ok")}
	cfg := testConfig("groq", "gemini")
	// An env var name that is certainly unset makes groq uncredentialed.
	cfg.Providers["groq"] = config.ProviderConfig{
		Model:     "groq-model",
		APIKeyEnv: "YONDERLLM_TEST_KEY_DEFINITELY_UNSET",
	}
	s := New(cfg, resolverFor(first, second))

	deltas, notices, done, err := collect(s.Ask(context.Background(), "hi"))
	if err != nil || !done {
		t.Fatalf("Ask: done=%v err=%v, want done with no error", done, err)
	}
	if got := strings.Join(deltas, ""); got != "ok" {
		t.Errorf("reply = %q, want %q", got, "ok")
	}
	if first.calls != 0 {
		t.Errorf("uncredentialed provider was called %d times, want 0", first.calls)
	}
	// Skipping happens before the attempt loop, so it is not a fallback and
	// produces no notice.
	if len(notices) != 0 {
		t.Errorf("notices = %v, want none", notices)
	}
}

func TestAskFailsWhenNoProviderIsCredentialed(t *testing.T) {
	p := &fakeProvider{name: "groq"}
	cfg := testConfig("groq")
	cfg.Providers["groq"] = config.ProviderConfig{
		Model:     "groq-model",
		APIKeyEnv: "YONDERLLM_TEST_KEY_DEFINITELY_UNSET",
	}
	s := New(cfg, resolverFor(p))

	_, _, _, err := collect(s.Ask(context.Background(), "hi"))
	if err == nil {
		t.Fatal("Ask succeeded with no usable credential")
	}
	if !strings.Contains(err.Error(), "API key") {
		t.Errorf("err = %v, want it to mention a missing API key", err)
	}
	if got := s.Usage().Requests(); got != 0 {
		t.Errorf("requests = %d, want 0 after refund", got)
	}
}

func TestAskCancelledContext(t *testing.T) {
	p := &fakeProvider{name: "groq", chunks: textChunks("ok")}
	s := New(testConfig("groq"), resolverFor(p))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, _, err := collect(s.Ask(ctx, "hi"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if p.calls != 0 {
		t.Errorf("provider called %d times on a cancelled context, want 0", p.calls)
	}
}

func TestAskSendsTrimmedPrompt(t *testing.T) {
	p := &fakeProvider{name: "groq", chunks: textChunks("ok")}
	s := New(testConfig("groq"), resolverFor(p))
	s.History().SetSystem("be brief")

	if _, _, _, err := collect(s.Ask(context.Background(), "hi")); err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}

	msgs := p.lastReq.Messages
	if len(msgs) != 2 {
		t.Fatalf("request had %d messages, want 2", len(msgs))
	}
	if msgs[0].Role != provider.RoleSystem || msgs[0].Content != "be brief" {
		t.Errorf("first message = %+v, want the system prompt", msgs[0])
	}
	if msgs[1].Role != provider.RoleUser || msgs[1].Content != "hi" {
		t.Errorf("second message = %+v, want the user turn", msgs[1])
	}
	if p.lastReq.Model != "groq-model" {
		t.Errorf("model = %q, want %q", p.lastReq.Model, "groq-model")
	}
	if p.lastReq.MaxTokens != config.DefaultMaxTokens {
		t.Errorf("MaxTokens = %d, want %d", p.lastReq.MaxTokens, config.DefaultMaxTokens)
	}
}

func TestPromptBudget(t *testing.T) {
	cfg := testConfig("groq")
	cfg.MaxTokens = 1000
	s := New(cfg, resolverFor(&fakeProvider{name: "groq"}))

	// Unknown window falls back to the conservative default.
	if got, want := s.promptBudget("groq"), defaultContextWindow-1000; got != want {
		t.Errorf("promptBudget with default window = %d, want %d", got, want)
	}

	s.SetContextWindow("groq", 32000)
	if got, want := s.promptBudget("groq"), 32000-1000; got != want {
		t.Errorf("promptBudget with known window = %d, want %d", got, want)
	}

	// A non-positive window is ignored rather than trusted.
	s.SetContextWindow("groq", 0)
	if got, want := s.promptBudget("groq"), 32000-1000; got != want {
		t.Errorf("promptBudget after a zero window = %d, want it unchanged at %d", got, want)
	}

	// With no output cap configured, the fixed reserve applies.
	cfg.MaxTokens = 0
	bare := New(cfg, resolverFor(&fakeProvider{name: "groq"}))
	if got, want := bare.promptBudget("groq"), defaultContextWindow-reservedForOutput; got != want {
		t.Errorf("promptBudget with no MaxTokens = %d, want %d", got, want)
	}

	// An output cap larger than the window leaves nothing, never a negative.
	cfg.MaxTokens = defaultContextWindow * 2
	greedy := New(cfg, resolverFor(&fakeProvider{name: "groq"}))
	if got := greedy.promptBudget("groq"); got != 0 {
		t.Errorf("promptBudget with an oversized cap = %d, want 0", got)
	}
}

func TestSetProviderAndModel(t *testing.T) {
	s := New(testConfig("groq", "gemini"), resolverFor(
		&fakeProvider{name: "groq"},
		&fakeProvider{name: "gemini"},
	))

	if got := s.Provider(); got != "groq" {
		t.Errorf("Provider() = %q, want %q", got, "groq")
	}
	if got := s.Model(); got != "groq-model" {
		t.Errorf("Model() = %q, want %q", got, "groq-model")
	}

	if err := s.SetProvider("gemini"); err != nil {
		t.Fatalf("SetProvider(gemini): %v", err)
	}
	if got := s.Provider(); got != "gemini" {
		t.Errorf("Provider() = %q, want %q", got, "gemini")
	}
	if got := s.Model(); got != "gemini-model" {
		t.Errorf("Model() = %q, want %q", got, "gemini-model")
	}

	if err := s.SetProvider("nope"); err == nil {
		t.Error("SetProvider accepted an unconfigured provider")
	}
	if got := s.Provider(); got != "gemini" {
		t.Errorf("a rejected SetProvider changed the active provider to %q", got)
	}

	s.SetModel("gemini-2.0-flash")
	if got := s.Model(); got != "gemini-2.0-flash" {
		t.Errorf("Model() = %q, want %q", got, "gemini-2.0-flash")
	}
}

func TestSetProviderNarrowsChainToConfiguredFallbacks(t *testing.T) {
	// Fallbacks are configuration, not history. Switching the active provider
	// to one of them leaves nothing behind it in the chain: the outgoing
	// provider was never listed as a fallback, so it is not tried.
	first := &fakeProvider{name: "groq", chunks: textChunks("from groq")}
	second := &fakeProvider{name: "gemini", err: &provider.QuotaError{Provider: "gemini"}}
	s := New(testConfig("groq", "gemini"), resolverFor(first, second))

	if err := s.SetProvider("gemini"); err != nil {
		t.Fatalf("SetProvider(gemini): %v", err)
	}

	_, _, done, err := collect(s.Ask(context.Background(), "hi"))
	if err == nil {
		t.Fatal("Ask succeeded, want the exchange to fail with the chain exhausted")
	}
	if !strings.Contains(err.Error(), "all providers failed") {
		t.Errorf("err = %v, want it to report the chain being exhausted", err)
	}
	if done {
		t.Error("Done was emitted for a failed exchange")
	}
	if second.calls != 1 {
		t.Errorf("gemini called %d times, want 1 (it is now active)", second.calls)
	}
	if first.calls != 0 {
		t.Errorf("groq called %d times, want 0: it is no longer active and was never a configured fallback", first.calls)
	}
}

func TestClearKeepsSystemPrompt(t *testing.T) {
	p := &fakeProvider{name: "groq", chunks: textChunks("ok")}
	s := New(testConfig("groq"), resolverFor(p))
	s.History().SetSystem("be brief")

	if _, _, _, err := collect(s.Ask(context.Background(), "hi")); err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	s.History().Clear()

	if got := s.History().Len(); got != 0 {
		t.Errorf("history has %d turns after Clear, want 0", got)
	}
	if got := s.History().System(); got != "be brief" {
		t.Errorf("system prompt = %q after Clear, want it kept", got)
	}
}

// Providers backs /model's switch list, so it must report every configured
// provider — not just the chain — in a stable order a user can read.
func TestProvidersListsEveryConfiguredNameSorted(t *testing.T) {
	s := New(testConfig("groq", "openrouter", "gemini"), resolverFor())

	got := s.Providers()
	want := []string{"gemini", "groq", "openrouter"}
	if !slices.Equal(got, want) {
		t.Errorf("Providers() = %v, want %v", got, want)
	}
}

// A session with a single provider still returns a list, so /model has nothing
// special to handle when there is nowhere to switch to.
func TestProvidersOnSingleProviderConfig(t *testing.T) {
	s := New(testConfig("groq"), resolverFor())

	if got := s.Providers(); !slices.Equal(got, []string{"groq"}) {
		t.Errorf("Providers() = %v, want [groq]", got)
	}
}

// Credentialed is what separates a provider a user may switch to from one that
// would fail on the next question, so it must track the key, not the config
// entry merely existing.
func TestSessionCredentialedReflectsProviderKeys(t *testing.T) {
	cfg := testConfig("groq", "gemini")
	// groq needs a key it does not have; gemini reaches a keyless endpoint.
	cfg.Providers["groq"] = config.ProviderConfig{Model: "groq-model", APIKeyEnv: "MISSING_KEY_ENV"}
	s := New(cfg, resolverFor())

	if s.Credentialed("groq") {
		t.Error("Credentialed(groq) = true, want false when its key env is unset")
	}
	if !s.Credentialed("gemini") {
		t.Error("Credentialed(gemini) = false, want true when no key is required")
	}
	if s.Credentialed("nope") {
		t.Error("Credentialed(nope) = true, want false for an unconfigured provider")
	}
}

// Clear is the session-level entry point /clear calls. It frees context without
// refunding the day's budget, which is the distinction worth pinning down.
func TestSessionClearDropsTurnsButKeepsUsage(t *testing.T) {
	p := &fakeProvider{name: "groq", chunks: textChunks("ok")}
	s := New(testConfig("groq"), resolverFor(p))
	s.History().SetSystem("be brief")

	if _, _, _, err := collect(s.Ask(context.Background(), "hi")); err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	before := s.Usage().Requests()

	s.Clear()

	if got := s.History().Len(); got != 0 {
		t.Errorf("history has %d turns after Clear, want 0", got)
	}
	if got := s.History().System(); got != "be brief" {
		t.Errorf("system prompt = %q after Clear, want it kept", got)
	}
	if got := s.Usage().Requests(); got != before {
		t.Errorf("requests = %d after Clear, want %d kept", got, before)
	}
}
