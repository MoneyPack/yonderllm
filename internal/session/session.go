package session

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sort"

	"yonderllm/internal/config"
	"yonderllm/internal/provider"
)

// defaultContextWindow is assumed when a provider does not report one. It is
// deliberately modest: guessing low costs a little context, guessing high
// costs a rejected request.
const defaultContextWindow = 8192

// reservedForOutput is the share of the window held back for the reply when a
// model's window is known but the caller set no explicit prompt budget.
const reservedForOutput = 1024

// Resolver supplies a live adapter for a provider name. The session takes this
// as a function rather than a map so that adapters are constructed lazily: a
// fallback provider that is never reached never has a client built for it.
type Resolver func(name string) (provider.Provider, error)

// Event is one thing that happened during an exchange. Fallbacks are reported
// as events rather than hidden, because a silent provider switch changes which
// model answered and the user is entitled to see that in the transcript.
type Event struct {
	// Delta is streamed assistant text, empty on non-text events.
	Delta string
	// Notice is a human-readable status line, such as a fallback switch.
	Notice string
	// Provider is the adapter that produced this event.
	Provider string
	// Done marks the final event of a successful exchange.
	Done bool
	// Usage carries token totals, set on the final event when reported.
	Usage *provider.Usage
}

// Session is one conversation bound to a configuration and a provider chain.
type Session struct {
	cfg      config.Config
	resolve  Resolver
	history  History
	usage    *Usage
	active   string
	contexts map[string]int
}

// New builds a session from resolved configuration.
func New(cfg config.Config, resolve Resolver) *Session {
	return &Session{
		cfg:      cfg,
		resolve:  resolve,
		usage:    NewUsage(cfg.DailyCap),
		active:   cfg.Provider,
		contexts: make(map[string]int),
	}
}

// History exposes the conversation for display and slash commands.
func (s *Session) History() *History { return &s.history }

// Usage exposes the counters backing /usage.
func (s *Session) Usage() *Usage { return s.usage }

// Provider reports the provider that will be tried first.
func (s *Session) Provider() string { return s.active }

// Providers lists every configured provider name in sorted order, so that
// /model can show what a user may switch to without them having to open the
// config file to find out.
func (s *Session) Providers() []string {
	out := make([]string, 0, len(s.cfg.Providers))
	for name := range s.cfg.Providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Credentialed reports whether a provider has a usable API key, which is what
// separates a provider a user can switch to from one that would fail on the
// next question.
func (s *Session) Credentialed(name string) bool { return s.cfg.Credentialed(name) }

// Clear drops the conversation turns, keeping the system prompt and every
// counter. /clear frees context, it does not refund the day's budget.
func (s *Session) Clear() { s.history.Clear() }

// Model reports the model id configured for the active provider.
func (s *Session) Model() string { return s.cfg.Providers[s.active].Model }

// SetProvider switches the preferred provider, as /model does. Fallbacks are
// unchanged, so a manual switch still degrades gracefully.
func (s *Session) SetProvider(name string) error {
	if _, ok := s.cfg.Providers[name]; !ok {
		return fmt.Errorf("provider %q is not configured", name)
	}
	s.active = name
	return nil
}

// SetModel overrides the model id for the active provider.
func (s *Session) SetModel(id string) {
	p := s.cfg.Providers[s.active]
	p.Model = id
	s.cfg.Providers[s.active] = p
}

// SetContextWindow records a model's window so trimming can use the real size
// instead of the conservative default.
func (s *Session) SetContextWindow(providerName string, tokens int) {
	if tokens > 0 {
		s.contexts[providerName] = tokens
	}
}

// promptBudget is how many tokens of history may be sent to providerName.
func (s *Session) promptBudget(providerName string) int {
	window, ok := s.contexts[providerName]
	if !ok || window <= 0 {
		window = defaultContextWindow
	}
	reserve := s.cfg.MaxTokens
	if reserve <= 0 {
		reserve = reservedForOutput
	}
	budget := window - reserve
	if budget < 0 {
		budget = 0
	}
	return budget
}

// chain returns the providers to try, active first, skipping any that have no
// usable credential. Attempting an uncredentialed provider would spend a
// round-trip to learn what config already knows.
func (s *Session) chain() []string {
	cfg := s.cfg
	cfg.Provider = s.active

	var out []string
	for _, name := range cfg.Chain() {
		if cfg.Credentialed(name) {
			out = append(out, name)
		}
	}
	return out
}

// Ask sends prompt and streams the reply, falling back across providers when
// one reports a quota or auth failure.
//
// The user turn is appended immediately so it appears in the transcript even
// if every provider fails. The assistant turn is appended only once a reply
// completes, so a failed exchange does not leave a truncated answer in history
// that would then be sent as context on the next question.
func (s *Session) Ask(ctx context.Context, prompt string) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		if err := s.usage.Reserve(); err != nil {
			yield(Event{}, err)
			return
		}

		s.history.Append(provider.RoleUser, prompt)

		candidates := s.chain()
		if len(candidates) == 0 {
			s.usage.Release()
			yield(Event{}, errors.New("no provider has a usable API key; set one of the provider key environment variables"))
			return
		}

		var errs []error
		for i, name := range candidates {
			if ctx.Err() != nil {
				s.usage.Release()
				yield(Event{}, ctx.Err())
				return
			}

			if i > 0 {
				notice := fmt.Sprintf("falling back to %s after %s failed", name, candidates[i-1])
				if !yield(Event{Notice: notice, Provider: name}, nil) {
					return
				}
			}

			reply, usage, err := s.streamOne(ctx, name, yield)
			switch {
			case err == nil:
				s.history.Append(provider.RoleAssistant, reply)
				if usage != nil {
					s.usage.Record(name, *usage)
				}
				yield(Event{Provider: name, Done: true, Usage: usage}, nil)
				return

			case errors.Is(err, errStopped):
				// The caller broke out of the loop; nothing more to do.
				return

			case errors.Is(err, provider.ErrQuota), errors.Is(err, provider.ErrAuth):
				// Recoverable by trying the next provider.
				errs = append(errs, err)

			default:
				// A transport or protocol failure is not something a
				// different provider is likely to fix, and retrying
				// would spend another free-tier request.
				s.usage.Release()
				yield(Event{Provider: name}, err)
				return
			}
		}

		s.usage.Release()
		yield(Event{}, fmt.Errorf("all providers failed: %w", errors.Join(errs...)))
	}
}

// errStopped reports that the consumer abandoned the sequence. It never
// escapes this package.
var errStopped = errors.New("session: consumer stopped")

// streamOne runs a single provider attempt, forwarding deltas through yield.
// It returns the assembled reply and the reported usage, or the error that
// ended the attempt.
func (s *Session) streamOne(ctx context.Context, name string, yield func(Event, error) bool) (string, *provider.Usage, error) {
	p, err := s.resolve(name)
	if err != nil {
		return "", nil, fmt.Errorf("initialising provider %s: %w", name, err)
	}

	pc := s.cfg.Providers[name]
	req := provider.Request{
		Model:     pc.Model,
		Messages:  s.history.Prompt(s.promptBudget(name)),
		MaxTokens: s.cfg.MaxTokens,
	}

	var (
		reply []byte
		usage *provider.Usage
	)
	for chunk, err := range p.Stream(ctx, req) {
		if err != nil {
			return "", nil, err
		}
		if chunk.Usage != nil {
			u := *chunk.Usage
			usage = &u
		}
		if chunk.Delta == "" {
			continue
		}
		reply = append(reply, chunk.Delta...)
		if !yield(Event{Delta: chunk.Delta, Provider: name}, nil) {
			return "", nil, errStopped
		}
	}
	return string(reply), usage, nil
}
