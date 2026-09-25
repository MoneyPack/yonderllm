package cli

import (
	"fmt"
	"sync"

	"github.com/MoneyPack/yonderllm/internal/adapters"
	"github.com/MoneyPack/yonderllm/internal/config"
	"github.com/MoneyPack/yonderllm/internal/perm"
	"github.com/MoneyPack/yonderllm/internal/provider"
	"github.com/MoneyPack/yonderllm/internal/session"
	"github.com/MoneyPack/yonderllm/internal/tools"
)

// This file is the composition root's narrowest part: the one function that
// turns configuration into a live adapter. Nothing below the cli package knows
// which concrete provider types exist, and session reaches them only through
// the [session.Resolver] built here; the adapter itself is built by the
// adapters package, which is the only code that knows the mapping from config
// fields to adapter options.

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

		// The field-to-option mapping lives in adapters so that this
		// resolver and the evaluation harness cannot drift apart on
		// which settings a provider honours.
		p := adapters.New(name, pc)
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

	// resolve has already rejected an unparseable mode, so this cannot fail;
	// the error is still returned rather than discarded because a future
	// change to resolve should not silently start defaulting to chat.
	mode, err := perm.ParseMode(cfg.Mode)
	if err != nil {
		return nil, err
	}
	// A nil approver: newSession serves the non-interactive paths — --json,
	// a pipe, a subcommand that prints and exits — where there is nobody to
	// put the question to. Capabilities the mode gates behind an approval
	// are withheld from the model entirely rather than offered and then
	// always refused. --yes is what lets such a run use them anyway: it
	// answers in advance, so there is nothing left to ask.
	return e.newSessionFor(cfg, mode, nil)
}

// newSessionFor builds a conversation from an already-resolved configuration
// and equips it with the tools mode permits.
//
// It exists so that the interactive interface, which needs the permission mode
// for its own display and therefore cannot go through newSession, still gets a
// session assembled identically. Attaching the tools here rather than at each
// call site means a mode can never reach a model with the wrong hands: there is
// one statement in the program that decides what a session may touch.
//
// approver may be nil, and that is the meaningful distinction between the
// callers: with one, a gated capability is offered and each use asks; without
// one, the capability never reaches the model. Passing it in rather than
// deciding here keeps the question of *who can be asked* with the caller that
// owns an interface, which is the only code able to answer it.
//
// It is a method because the policy depends on --yes, which lives on the
// environment. The flag is read here, at the single statement that decides what
// a session may touch, rather than at either call site: an answer given in
// advance is still an answer, and both interfaces should honour it the same way.
func (e *env) newSessionFor(cfg config.Config, mode perm.Mode, approver tools.Approver) (*session.Session, error) {
	// resolve has already refused --yes outside agent mode, so this cannot
	// hand auto-approval to chat or code.
	policy := perm.New(mode)
	if e.yes {
		policy = perm.NewAutoApprove(mode)
	}

	path, err := session.DefaultUsagePath()
	if err != nil {
		return nil, fmt.Errorf("locate daily usage storage: %w", err)
	}
	// The session gets a value copied out of cfg rather than cfg itself:
	// per-provider model and context_window settings travel in, and a
	// /model switch made later stays inside the session. This is also
	// where a configured context_window starts governing trimming.
	sess := session.NewWithOptions(session.OptionsFromConfig(cfg), newResolver(cfg))
	sess.SetUsage(session.NewPersistentUsage(cfg.DailyCap, path))
	sess.SetTools(tools.For(policy, approver, tools.HideEnv(secretEnvNames(cfg)...))...)
	return sess, nil
}

// secretEnvNames lists every environment variable the configuration says
// holds a credential: each provider's api_key_env and header_env. They are
// handed to the tools so that a command the model runs never inherits them;
// the configuration is the one place that knows which names those are.
func secretEnvNames(cfg config.Config) []string {
	var names []string
	for _, pc := range cfg.Providers {
		if pc.APIKeyEnv != "" {
			names = append(names, pc.APIKeyEnv)
		}
		for _, env := range pc.HeaderEnv {
			names = append(names, env)
		}
	}
	return names
}
