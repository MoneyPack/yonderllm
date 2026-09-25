package session

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MoneyPack/yonderllm/internal/config"
	"github.com/MoneyPack/yonderllm/internal/provider"
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
	return []provider.Model{{ID: "fake-model", Name: "Fake", Pricing: provider.Pricing{Known: true}}}, nil
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

func TestAskFallsBackWhenProviderIsUnavailable(t *testing.T) {
	first := &fakeProvider{name: "groq", err: &provider.UnavailableError{Provider: "groq", Status: 503}}
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
}

func TestFallbackNoticeNamesWhyProviderWasSkipped(t *testing.T) {
	first := &fakeProvider{name: "groq", err: &provider.QuotaError{Provider: "groq", RetryAfter: time.Minute}}
	second := &fakeProvider{name: "gemini", chunks: textChunks("ok")}
	s := New(testConfig("groq", "gemini"), resolverFor(first, second))
	_, notices, _, err := collect(s.Ask(context.Background(), "hi"))
	if err != nil || len(notices) != 1 || !strings.Contains(notices[0], "quota") || !strings.Contains(notices[0], "1m") {
		t.Fatalf("fallback notices = %v, error=%v", notices, err)
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

func TestAskAsksEachProviderForItsOwnModel(t *testing.T) {
	// Model names do not travel across a fallback: each provider has its own
	// catalogue, so the second attempt must ask for the second provider's
	// model rather than repeating the first one's.
	first := &fakeProvider{name: "groq", err: &provider.QuotaError{Provider: "groq"}}
	second := &fakeProvider{name: "gemini", chunks: textChunks("ok")}
	s := New(testConfig("groq", "gemini"), resolverFor(first, second))

	deltas, _, _, err := collect(s.Ask(context.Background(), "hi"))
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if got := strings.Join(deltas, ""); got != "ok" {
		t.Errorf("reply = %q, want %q", got, "ok")
	}
	if got := first.lastReq.Model; got != "groq-model" {
		t.Errorf("groq was asked for model %q, want %q", got, "groq-model")
	}
	if got := second.lastReq.Model; got != "gemini-model" {
		t.Errorf("gemini was asked for model %q, want %q", got, "gemini-model")
	}
}

func TestAskSkipsProvidersWithNoModel(t *testing.T) {
	// A provider with no model configured is refused locally, before any bytes
	// leave the machine: it spends nothing from the free-tier request budget,
	// and the chain carries on to a provider that can answer.
	cfg := testConfig("groq", "gemini")
	cfg.Providers["groq"] = config.ProviderConfig{}

	first := &fakeProvider{name: "groq", chunks: textChunks("never asked")}
	second := &fakeProvider{name: "gemini", chunks: textChunks("ok")}
	s := New(cfg, resolverFor(first, second))

	deltas, notices, done, err := collect(s.Ask(context.Background(), "hi"))
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if got := strings.Join(deltas, ""); got != "ok" {
		t.Errorf("reply = %q, want %q", got, "ok")
	}
	if !done {
		t.Error("Done was not emitted for a successful exchange")
	}
	if first.calls != 0 {
		t.Errorf("groq called %d times, want 0: with no model there is nothing to ask for", first.calls)
	}
	if second.calls != 1 {
		t.Errorf("gemini called %d times, want 1", second.calls)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "gemini") {
		t.Errorf("notices = %v, want one announcing the fallback to gemini", notices)
	}
}

func TestAskNamesTheProviderThatHasNoModel(t *testing.T) {
	// With nowhere left to fall back to, the missing model has to be legible:
	// a typed error naming the provider, not the bare 400 a remote endpoint
	// returns for a request with an empty model.
	cfg := testConfig("groq")
	cfg.Providers["groq"] = config.ProviderConfig{}
	p := &fakeProvider{name: "groq", chunks: textChunks("never asked")}
	s := New(cfg, resolverFor(p))

	_, _, done, err := collect(s.Ask(context.Background(), "hi"))
	if err == nil {
		t.Fatal("Ask succeeded with no model configured")
	}
	if !errors.Is(err, provider.ErrNoModel) {
		t.Errorf("err = %v, want it to match provider.ErrNoModel", err)
	}
	if !strings.Contains(err.Error(), "groq") {
		t.Errorf("err = %v, want it to name the provider that is short a model", err)
	}
	if done {
		t.Error("Done was emitted for a failed exchange")
	}
	if p.calls != 0 {
		t.Errorf("groq called %d times, want 0", p.calls)
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

// scriptedProvider answers each request from the next entry in its script, so a
// test can drive an exchange that takes several rounds. A script shorter than
// the number of requests repeats its last entry, which is how a model that
// never stops asking for tools is written.
type scriptedProvider struct {
	name   string
	script [][]provider.Chunk
	reqs   []provider.Request
}

func (s *scriptedProvider) Name() string { return s.name }

func (s *scriptedProvider) Models(ctx context.Context) ([]provider.Model, error) {
	return []provider.Model{{ID: "scripted-model", Name: "Scripted", Pricing: provider.Pricing{Known: true}}}, nil
}

func (s *scriptedProvider) Stream(ctx context.Context, req provider.Request) iter.Seq2[provider.Chunk, error] {
	s.reqs = append(s.reqs, req)
	round := len(s.reqs) - 1
	if round >= len(s.script) {
		round = len(s.script) - 1
	}
	chunks := s.script[round]
	return func(yield func(provider.Chunk, error) bool) {
		for _, c := range chunks {
			if !yield(c, nil) {
				return
			}
		}
	}
}

// resolverOf maps any set of adapters onto their names, so a test can mix a
// scripted provider with one that only fails.
func resolverOf(ps ...provider.Provider) Resolver {
	byName := make(map[string]provider.Provider, len(ps))
	for _, p := range ps {
		byName[p.Name()] = p
	}
	return func(name string) (provider.Provider, error) {
		p, ok := byName[name]
		if !ok {
			return nil, errors.New("no such provider: " + name)
		}
		return p, nil
	}
}

// toolCallChunk is the shape an adapter hands up once it has reassembled a
// call: whole, and carrying the finish reason that explains it.
func toolCallChunk(id, name, arguments string) provider.Chunk {
	return provider.Chunk{
		ToolCalls: []provider.ToolCall{{ID: id, Name: name, Arguments: arguments}},
		Finish:    provider.FinishTool,
	}
}

// stubTool wraps a run function in the definition the model would be shown.
func stubTool(name string, run func(ctx context.Context, arguments string) (string, error)) Tool {
	return Tool{
		Definition: provider.Tool{
			Name:        name,
			Description: "stub " + name,
			Parameters:  json.RawMessage(`{"type":"object"}`),
		},
		Run: run,
	}
}

// exchange is everything one Ask produced, in the order it arrived.
type exchange struct {
	deltas  []string
	notices []string
	runs    []ToolRun
	usage   *provider.Usage
	done    bool
}

// collectTools drains an exchange, keeping the tool events collect discards.
func collectTools(seq iter.Seq2[Event, error]) (exchange, error) {
	var ex exchange
	for ev, err := range seq {
		if err != nil {
			return ex, err
		}
		if ev.Delta != "" {
			ex.deltas = append(ex.deltas, ev.Delta)
		}
		if ev.Notice != "" {
			ex.notices = append(ex.notices, ev.Notice)
		}
		if ev.Tool != nil {
			ex.runs = append(ex.runs, *ev.Tool)
		}
		if ev.Done {
			ex.done = true
			ex.usage = ev.Usage
		}
	}
	return ex, nil
}

// toolNames lists what a request advertised, which is how a test checks the
// model was offered the tools rather than merely that some were registered.
func toolNames(req provider.Request) []string {
	out := make([]string, 0, len(req.Tools))
	for _, t := range req.Tools {
		out = append(out, t.Name)
	}
	return out
}

// A tool call is not an answer: the session has to run the tool, hand the result
// back and let the model continue, and only the prose from that second round is
// the reply. This is the whole point of the loop, so it is asserted end to end.
func TestAskRunsAToolThenStreamsTheReply(t *testing.T) {
	var gotArgs string
	p := &scriptedProvider{
		name: "groq",
		script: [][]provider.Chunk{
			{
				toolCallChunk("call-1", "read", `{"path":"go.mod"}`),
				{Usage: &provider.Usage{PromptTokens: 10, CompletionTokens: 4}},
			},
			append(textChunks("The module ", "is yonderllm."),
				provider.Chunk{Finish: provider.FinishStop, Usage: &provider.Usage{PromptTokens: 20, CompletionTokens: 3}}),
		},
	}
	s := New(testConfig("groq"), resolverOf(p))
	s.SetTools(stubTool("read", func(ctx context.Context, arguments string) (string, error) {
		gotArgs = arguments
		return "module yonderllm", nil
	}))

	ex, err := collectTools(s.Ask(context.Background(), "what module is this"))
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if !ex.done {
		t.Error("exchange did not finish")
	}
	if want := []string{"The module ", "is yonderllm."}; !slices.Equal(ex.deltas, want) {
		t.Errorf("deltas = %v, want %v", ex.deltas, want)
	}

	// The arguments reach the tool as the model wrote them, unvalidated.
	if want := `{"path":"go.mod"}`; gotArgs != want {
		t.Errorf("tool received arguments %q, want %q", gotArgs, want)
	}

	// A run is bracketed: one event before it starts so the transcript can
	// show it working, one after so it can show what came back.
	if len(ex.runs) != 2 {
		t.Fatalf("got %d tool events, want 2 (start and finish)", len(ex.runs))
	}
	start, done := ex.runs[0], ex.runs[1]
	if start.Finished {
		t.Error("first tool event is marked finished, want the start of the run")
	}
	if start.ID != "call-1" || start.Name != "read" {
		t.Errorf("start event = %+v, want call-1/read", start)
	}
	if !done.Finished {
		t.Error("second tool event is not marked finished")
	}
	if done.Result != "module yonderllm" || done.Err != "" {
		t.Errorf("finish event = %+v, want the tool's result and no error", done)
	}

	// Two requests, and the second must carry the result back or the model
	// has nothing to continue from.
	if len(p.reqs) != 2 {
		t.Fatalf("provider saw %d requests, want 2", len(p.reqs))
	}
	if want := []string{"read"}; !slices.Equal(toolNames(p.reqs[0]), want) {
		t.Errorf("first request advertised %v, want %v", toolNames(p.reqs[0]), want)
	}
	sent := p.reqs[1].Messages
	result := sent[len(sent)-1]
	if result.Role != provider.RoleTool || result.ToolCallID != "call-1" || result.Content != "module yonderllm" {
		t.Errorf("last message of the second request = %+v, want the tool result for call-1", result)
	}

	// History has to read back as the exchange happened: question, the call,
	// its result, then the answer.
	turns := s.History().Turns()
	if len(turns) != 4 {
		t.Fatalf("history has %d turns, want 4", len(turns))
	}
	if turns[1].Role != provider.RoleAssistant || len(turns[1].ToolCalls) != 1 {
		t.Errorf("turn 2 = %+v, want the assistant's tool call", turns[1])
	}
	if turns[2].Role != provider.RoleTool || turns[2].ToolCallID != "call-1" {
		t.Errorf("turn 3 = %+v, want the tool result", turns[2])
	}
	if got := turns[3].Content; got != "The module is yonderllm." {
		t.Errorf("final turn = %q, want the streamed reply", got)
	}

	// The tokens reported at the end are what the question cost in total,
	// not what its last leg cost.
	if ex.usage == nil {
		t.Fatal("done event carried no usage")
	}
	if ex.usage.PromptTokens != 30 || ex.usage.CompletionTokens != 7 {
		t.Errorf("usage = %+v, want both rounds summed (30/7)", *ex.usage)
	}
}

// The daily cap counts questions, not remote requests: the user asked once and
// the number of rounds is the session's decision. The per-provider tally still
// counts every request, because that is what the free tier is spending.
func TestAskChargesOneReservationForAWholeToolExchange(t *testing.T) {
	p := &scriptedProvider{
		name: "groq",
		script: [][]provider.Chunk{
			{
				toolCallChunk("call-1", "read", "{}"),
				{Usage: &provider.Usage{PromptTokens: 10, CompletionTokens: 4}},
			},
			append(textChunks("done"),
				provider.Chunk{Finish: provider.FinishStop, Usage: &provider.Usage{PromptTokens: 20, CompletionTokens: 1}}),
		},
	}
	s := New(testConfig("groq"), resolverOf(p))
	s.SetTools(stubTool("read", func(ctx context.Context, arguments string) (string, error) {
		return "contents", nil
	}))

	if _, err := collectTools(s.Ask(context.Background(), "read it")); err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}

	if got := s.Usage().Requests(); got != 1 {
		t.Errorf("capped requests = %d, want 1 for one question", got)
	}
	if got := s.Usage().ByProvider()["groq"].Requests; got != 2 {
		t.Errorf("groq requests = %d, want 2, one per round", got)
	}
	if got := s.Usage().ByProvider()["groq"].PromptTokens; got != 30 {
		t.Errorf("groq prompt tokens = %d, want 30 across both rounds", got)
	}
}

// The round bound is a nudge, not a wall. On the last round the tools are
// withheld so the model has to answer in prose, and any call it makes anyway is
// ignored rather than obeyed — otherwise the exchange could end with no answer.
func TestAskWithholdsToolsOnTheLastRound(t *testing.T) {
	runs := 0
	// One round, repeated: a model that asks for a tool every single time.
	p := &scriptedProvider{
		name: "groq",
		script: [][]provider.Chunk{{
			{Delta: "still looking"},
			toolCallChunk("call-x", "read", "{}"),
		}},
	}
	s := New(testConfig("groq"), resolverOf(p))
	s.SetTools(stubTool("read", func(ctx context.Context, arguments string) (string, error) {
		runs++
		return "nothing useful", nil
	}))

	ex, err := collectTools(s.Ask(context.Background(), "keep going"))
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if !ex.done {
		t.Error("exchange did not finish, want the last round to answer")
	}
	if len(p.reqs) != maxToolRounds {
		t.Fatalf("provider saw %d requests, want %d", len(p.reqs), maxToolRounds)
	}
	if got := toolNames(p.reqs[maxToolRounds-2]); len(got) != 1 {
		t.Errorf("second-to-last request advertised %v, want the tools still offered", got)
	}
	if got := p.reqs[maxToolRounds-1].Tools; got != nil {
		t.Errorf("last request advertised %v, want no tools", toolNames(p.reqs[maxToolRounds-1]))
	}
	if want := maxToolRounds - 1; runs != want {
		t.Errorf("tool ran %d times, want %d, once per round but the last", runs, want)
	}

	// The prose from the final round is the reply, and the call it made
	// alongside left no trace.
	turns := s.History().Turns()
	final := turns[len(turns)-1]
	if final.Role != provider.RoleAssistant || final.Content != "still looking" || len(final.ToolCalls) != 0 {
		t.Errorf("final turn = %+v, want the last round's prose with no calls", final)
	}
}

// Models occasionally invent a tool. The recoverable answer is to tell the model
// so as the call's result, because it is the only party that can correct itself.
func TestAskReportsAnUnknownToolBackToTheModel(t *testing.T) {
	p := &scriptedProvider{
		name: "groq",
		script: [][]provider.Chunk{
			{toolCallChunk("call-1", "compile", "{}")},
			append(textChunks("sorry"), provider.Chunk{Finish: provider.FinishStop}),
		},
	}
	s := New(testConfig("groq"), resolverOf(p))
	s.SetTools(stubTool("read", func(ctx context.Context, arguments string) (string, error) {
		return "contents", nil
	}))

	ex, err := collectTools(s.Ask(context.Background(), "compile it"))
	if err != nil {
		t.Fatalf("Ask returned error: %v, want the exchange to survive an invented tool", err)
	}
	if !ex.done {
		t.Error("exchange did not finish")
	}
	if len(ex.runs) != 2 {
		t.Fatalf("got %d tool events, want 2", len(ex.runs))
	}
	want := `no tool named "compile" is available`
	if got := ex.runs[1].Err; got != want {
		t.Errorf("finish event error = %q, want %q", got, want)
	}

	turns := s.History().Turns()
	if got := turns[2]; got.Role != provider.RoleTool || got.Content != want {
		t.Errorf("turn 3 = %+v, want the failure recorded as the call's result", got)
	}
}

// A tool that fails is reported as its own result rather than raised as an
// error: the exchange continues, and the model gets to see what went wrong. A
// call left without a matching result would be rejected outright next round.
func TestAskReportsAFailingToolAsItsResult(t *testing.T) {
	p := &scriptedProvider{
		name: "groq",
		script: [][]provider.Chunk{
			{toolCallChunk("call-1", "read", `{"path":"ghost.txt"}`)},
			append(textChunks("no such file"), provider.Chunk{Finish: provider.FinishStop}),
		},
	}
	s := New(testConfig("groq"), resolverOf(p))
	s.SetTools(stubTool("read", func(ctx context.Context, arguments string) (string, error) {
		return "", errors.New("workspace: no file named ghost.txt")
	}))

	ex, err := collectTools(s.Ask(context.Background(), "read ghost.txt"))
	if err != nil {
		t.Fatalf("Ask returned error: %v, want a tool failure to stay inside the exchange", err)
	}
	if len(ex.runs) != 2 {
		t.Fatalf("got %d tool events, want 2", len(ex.runs))
	}
	done := ex.runs[1]
	if done.Err != "workspace: no file named ghost.txt" || done.Result != "" {
		t.Errorf("finish event = %+v, want the error and no result", done)
	}
	if got := s.History().Turns()[2]; got.Role != provider.RoleTool || got.Content != done.Err {
		t.Errorf("turn 3 = %+v, want the error handed back as the result", got)
	}
	if !ex.done {
		t.Error("exchange did not finish after a failing tool")
	}
}

// A provider that was skipped over once will be skipped over again. Retrying a
// spent free tier on every round would burn the allowance to learn what the
// previous round already proved, so the chain narrows to whoever answered.
func TestAskNarrowsTheChainAfterAToolRound(t *testing.T) {
	spent := &fakeProvider{name: "groq", err: provider.ErrQuota}
	answering := &scriptedProvider{
		name: "gemini",
		script: [][]provider.Chunk{
			{toolCallChunk("call-1", "read", "{}")},
			append(textChunks("answered"), provider.Chunk{Finish: provider.FinishStop}),
		},
	}
	s := New(testConfig("groq", "gemini"), resolverOf(spent, answering))
	s.SetTools(stubTool("read", func(ctx context.Context, arguments string) (string, error) {
		return "contents", nil
	}))

	ex, err := collectTools(s.Ask(context.Background(), "read it"))
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if spent.calls != 1 {
		t.Errorf("exhausted provider was tried %d times, want 1", spent.calls)
	}
	if len(answering.reqs) != 2 {
		t.Errorf("answering provider saw %d requests, want 2", len(answering.reqs))
	}
	// Only the first round has a fallback to announce; by the second, gemini
	// heads the chain and there is nothing to explain.
	if len(ex.notices) != 1 {
		t.Errorf("notices = %v, want one fallback announcement", ex.notices)
	}
	if len(ex.notices) == 1 && !strings.Contains(ex.notices[0], "falling back to gemini") {
		t.Errorf("notice = %q, want it to name the provider taking over", ex.notices[0])
	}
}

// A consumer that stops mid-run must not have the tool run behind its back, and
// the request it already spent is not refunded: the remote call was made.
func TestAskStopsWhenConsumerBreaksOnAToolEvent(t *testing.T) {
	runs := 0
	p := &scriptedProvider{
		name:   "groq",
		script: [][]provider.Chunk{{toolCallChunk("call-1", "read", "{}")}},
	}
	s := New(testConfig("groq"), resolverOf(p))
	s.SetTools(stubTool("read", func(ctx context.Context, arguments string) (string, error) {
		runs++
		return "contents", nil
	}))

	var seen int
	for ev, err := range s.Ask(context.Background(), "read it") {
		if err != nil {
			t.Fatalf("Ask returned error: %v", err)
		}
		if ev.Tool != nil {
			seen++
			break
		}
	}

	if seen != 1 {
		t.Fatalf("saw %d tool events before breaking, want 1", seen)
	}
	if runs != 0 {
		t.Errorf("tool ran %d times, want 0: the consumer left before it started", runs)
	}
	if got := s.Usage().Requests(); got != 1 {
		t.Errorf("requests = %d, want the spent request kept at 1", got)
	}
}

// SetTools replaces the whole set rather than adding to it, because the set is
// decided by the permission mode and a mode change has to be able to take a
// capability away. An empty set must vanish from the request entirely: some
// providers reject an empty tool list rather than reading it as no tools.
func TestSetToolsReplacesTheWholeSet(t *testing.T) {
	noop := func(ctx context.Context, arguments string) (string, error) { return "", nil }
	s := New(testConfig("groq"), resolverOf())

	if got := s.definitions(); got != nil {
		t.Errorf("definitions() = %v on a fresh session, want nil", got)
	}

	s.SetTools(stubTool("read", noop), stubTool("search", noop))
	got := make([]string, 0, 2)
	for _, d := range s.definitions() {
		got = append(got, d.Name)
	}
	if want := []string{"read", "search"}; !slices.Equal(got, want) {
		t.Errorf("definitions() = %v, want %v", got, want)
	}

	// Narrowing the mode narrows the set, so what was granted can be taken.
	s.SetTools(stubTool("read", noop))
	if defs := s.definitions(); len(defs) != 1 || defs[0].Name != "read" {
		t.Errorf("definitions() = %v after replacing, want just read", defs)
	}

	s.SetTools()
	if got := s.definitions(); got != nil {
		t.Errorf("definitions() = %v after clearing, want nil so the key is omitted", got)
	}
}

// The parameter schema is the model's only description of what a tool expects,
// so it has to reach the provider byte for byte rather than round-tripped
// through a Go type that might drop a field it does not know about.
func TestAskPassesToolSchemaThroughUntouched(t *testing.T) {
	schema := `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`
	p := &scriptedProvider{
		name:   "groq",
		script: [][]provider.Chunk{append(textChunks("ok"), provider.Chunk{Finish: provider.FinishStop})},
	}
	s := New(testConfig("groq"), resolverOf(p))
	s.SetTools(Tool{
		Definition: provider.Tool{
			Name:        "read",
			Description: "read a file from the workspace",
			Parameters:  json.RawMessage(schema),
		},
		Run: func(ctx context.Context, arguments string) (string, error) { return "", nil },
	})

	if _, err := collectTools(s.Ask(context.Background(), "hi")); err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if len(p.reqs) != 1 {
		t.Fatalf("provider saw %d requests, want 1", len(p.reqs))
	}
	sent := p.reqs[0].Tools
	if len(sent) != 1 {
		t.Fatalf("request advertised %d tools, want 1", len(sent))
	}
	if got := string(sent[0].Parameters); got != schema {
		t.Errorf("parameters = %s, want %s", got, schema)
	}
	if sent[0].Description != "read a file from the workspace" {
		t.Errorf("description = %q, want it forwarded", sent[0].Description)
	}
}

// SaveConversation and LoadConversation must round-trip through a *Sessions
// store: a resumed session carries the same turns and system prompt, and a
// re-saved resume is identical (idempotent persistence).
func TestSaveLoadConversationRoundTrip(t *testing.T) {
	s1 := New(testConfig("groq"), func(string) (provider.Provider, error) { return nil, nil })
	s1.history.SetSystem("be terse")
	s1.history.Append(provider.RoleUser, "hello")
	s1.history.Append(provider.RoleAssistant, "hi")

	dir := t.TempDir()
	store, err := OpenSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("two", s1.SaveConversation()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Get("two")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Restore into a second, empty session.
	s2 := New(testConfig("groq"), func(string) (provider.Provider, error) { return nil, nil })
	if err := s2.LoadConversation(got); err != nil {
		t.Fatalf("LoadConversation: %v", err)
	}
	if got := s2.History().System(); got != "be terse" {
		t.Errorf("system = %q", got)
	}
	if got := s2.History().Len(); got != 2 {
		t.Errorf("turns = %d, want 2", got)
	}
	turns := s2.History().Turns()
	if turns[0].Role != provider.RoleUser || turns[0].Content != "hello" {
		t.Errorf("turn0 = %+v", turns[0])
	}
	if turns[1].Role != provider.RoleAssistant || turns[1].Content != "hi" {
		t.Errorf("turn1 = %+v", turns[1])
	}
}
