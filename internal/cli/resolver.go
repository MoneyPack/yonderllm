package cli

import (
	"fmt"
	"sync"

	"yonderllm/internal/config"
	"yonderllm/internal/provider"
	"yonderllm/internal/session"
)

// This file is the composition root's narrowest part: the one function that
// turns configuration into a live adapter. Nothing below the cli package knows
// which concrete provider types exist, and session reaches them only through
// the [session.Resolver] built here.

// newResolver returns a resolver over cfg.
//
// Adapters are built lazily and memoised. Laziness matters because a session's
// fallback chain usually names providers that are never reached: constructing
// every adapter up front would demand credentials for backends the run will
// not touch. Memoising matters because the resolver is called once per attempt,
// including on every fallback, and an adapter owns an HTTP client whose
// connection pool should outlive a single request.
func newResolver(cfg config.Config) session.Resolver {
	var (
		mu    sync.Mutex
		cache = make(map[string]provider.Provider)
	)

	return func(name string) (provider.Provider, error) {
		mu.Lock()
		defer mu.Unlock()

		if p, ok := cache[name]; ok {
			return p, nil
		}

		pc, ok := cfg.Providers[name]
		if !ok {
			return nil, fmt.Errorf("provider %q is not configured", name)
		}
		if !cfg.Credentialed(name) {
			// Reported as an auth error rather than a plain one so that
			// session treats it as a reason to try the next provider in
			// the chain instead of aborting the exchange.
			return nil, &provider.AuthError{
				Provider: name,
				Reason:   fmt.Sprintf("no API key: set %s", credentialHint(pc)),
			}
		}
		if pc.BaseURL == "" {
			return nil, fmt.Errorf("provider %q has no base_url", name)
		}

		// Every provider yonderllm ships with speaks the [OI]
		// chat-completions dialect, so one adapter covers them all. A
		// backend that does not fit gets its own constructor here, and
		// nothing outside this switch has to change.
		p := provider.NewChatCompat(name, pc.BaseURL, pc.APIKey())
		cache[name] = p
		return p, nil
	}
}

// credentialHint names the environment variable a user should set. Printing the
// variable's name is safe and actionable; printing anything derived from its
// value would not be.
func credentialHint(pc config.ProviderConfig) string {
	if pc.APIKeyEnv != "" {
		return pc.APIKeyEnv
	}
	return "the provider's api_key_env"
}

// newSession wires a resolved configuration into a conversation. Subcommands
// that talk to a model all start here, so the construction order — resolver
// first, session second — exists in exactly one place.
func (e *env) newSession() (*session.Session, error) {
	cfg, err := e.resolve()
	if err != nil {
		return nil, err
	}
	return session.New(cfg, newResolver(cfg)), nil
}
