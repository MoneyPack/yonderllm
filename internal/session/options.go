package session

import (
	"maps"
	"slices"

	"github.com/MoneyPack/yonderllm/internal/config"
)

// ProviderOptions is what the session needs to know about one provider. It is
// deliberately narrower than [config.ProviderConfig]: the session never sees a
// base URL, a header or a key, because talking to the backend is the
// resolver's job and the session only decides which backend to ask.
type ProviderOptions struct {
	// Model is the id requested from this provider. Empty is allowed and is
	// refused locally at the first attempt, with an error naming the
	// provider, rather than sent to a backend that would answer with a
	// bare 400.
	Model string
	// Credentialed reports whether the provider can be called at all. One
	// that cannot is skipped in the chain rather than attempted, because
	// trying it would spend a round-trip to learn what the caller already
	// knows.
	Credentialed bool
	// ContextWindow is the model's window in tokens. Zero means unknown,
	// and history is then trimmed to a conservative default rather than a
	// guess.
	ContextWindow int
}

// Options is everything a Session takes from configuration, as a value the
// session owns outright.
//
// It exists so that a session never holds a [config.Config]: the config's
// Providers map is shared with every other part of the program that read it,
// and a session that rewrote a model into that map would change what the
// providers subcommand reports and what the next session starts with. Copying
// the few fields the session needs into its own value keeps a /model switch
// where it belongs. It also spares library callers — the evaluation harness,
// tests — from fabricating a whole Config to get a session.
type Options struct {
	// Provider is the name tried first.
	Provider string
	// Fallbacks are tried in order when the active provider reports a quota
	// or auth failure. The active provider is never tried twice even if it
	// is listed here.
	Fallbacks []string
	// MaxTokens caps output tokens per request. It also sets how much of a
	// context window is held back for the reply when trimming history.
	MaxTokens int
	// DailyCap limits requests per day for the in-memory counter New
	// installs. Zero disables the cap. A caller that attaches its own
	// counter with SetUsage supplies the cap there instead.
	DailyCap int
	// RetryAttempts is how many times a quota or 5xx failure is retried on
	// the same provider before falling back, when no output has arrived.
	RetryAttempts int
	// RetryBackoffMS is the pause between those retries, unless the
	// provider asked for a longer one.
	RetryBackoffMS int
	// RequestTimeoutSeconds bounds one whole exchange, including every tool
	// round. Zero disables it and leaves deadlines to the caller's context.
	RequestTimeoutSeconds int
	// Providers holds every provider the session may switch to, keyed by
	// name. Provider and every Fallback must appear here.
	Providers map[string]ProviderOptions
}

// OptionsFromConfig copies the session-relevant parts of a resolved
// configuration. Credentials are reduced to a yes/no: the session needs to
// know whether a provider is usable, never the key itself.
func OptionsFromConfig(cfg config.Config) Options {
	providers := make(map[string]ProviderOptions, len(cfg.Providers))
	for name, pc := range cfg.Providers {
		providers[name] = ProviderOptions{
			Model:         pc.Model,
			Credentialed:  cfg.Credentialed(name),
			ContextWindow: pc.ContextWindow,
		}
	}
	return Options{
		Provider:              cfg.Provider,
		Fallbacks:             slices.Clone(cfg.Fallbacks),
		MaxTokens:             cfg.MaxTokens,
		DailyCap:              cfg.DailyCap,
		RetryAttempts:         cfg.RetryAttempts,
		RetryBackoffMS:        cfg.RetryBackoffMS,
		RequestTimeoutSeconds: cfg.RequestTimeoutSeconds,
		Providers:             providers,
	}
}

// New builds a session from resolved configuration. It is a convenience over
// [NewWithOptions] for callers that already hold a [config.Config]; the config
// is read once here and never touched again.
func New(cfg config.Config, resolve Resolver) *Session {
	return NewWithOptions(OptionsFromConfig(cfg), resolve)
}

// NewWithOptions builds a session from opts. The maps and slices in opts are
// copied, so the caller may go on using or mutating its own value without
// reaching into the session.
func NewWithOptions(opts Options, resolve Resolver) *Session {
	opts.Providers = maps.Clone(opts.Providers)
	if opts.Providers == nil {
		opts.Providers = map[string]ProviderOptions{}
	}
	opts.Fallbacks = slices.Clone(opts.Fallbacks)
	return &Session{
		opts:    opts,
		resolve: resolve,
		usage:   NewUsage(opts.DailyCap),
		active:  opts.Provider,
	}
}
