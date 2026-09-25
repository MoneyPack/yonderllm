// Package adapters turns a provider's configuration into a live adapter.
//
// It is the one place that knows how the fields of a [config.ProviderConfig]
// map onto the options a [provider.ChatCompat] takes. That mapping used to be
// copied at every construction site, and one of the copies had already
// forgotten omit_stream_options; keeping it here means a new provider setting
// is wired up once and every caller — the CLI, the evaluation harness — picks
// it up together.
//
// The package deliberately does not decide whether an adapter should be built.
// Checking that a provider is configured, credentialed and has a base URL is
// policy that belongs to the caller: the CLI wants a typed auth error the
// session can fall back on, while the harness wants to refuse up front.
package adapters

import (
	"os"

	"github.com/MoneyPack/yonderllm/internal/config"
	"github.com/MoneyPack/yonderllm/internal/provider"
)

// New builds the adapter for a configured provider. name is the provider's
// key in the configuration and is what errors and events will call it. extra
// options are applied after the ones derived from pc, so a caller can still
// install its own HTTP client or header.
//
// Every provider yonderllm ships with speaks the chat-completions dialect, so
// one adapter type covers them all. A backend that does not fit gets its own
// branch here, and nothing outside this function has to change.
func New(name string, pc config.ProviderConfig, extra ...provider.ChatOption) provider.Provider {
	opts := make([]provider.ChatOption, 0, len(pc.HeaderEnv)+1+len(extra))
	// Header values are read from the environment at construction, not at
	// request time: an operator rotating a tenant token expects the running
	// process to keep the credential it started with, and reading per
	// request would make a mid-session rotation half-apply.
	for header, env := range pc.HeaderEnv {
		opts = append(opts, provider.WithHeader(header, os.Getenv(env)))
	}
	if pc.OmitStreamOptions {
		opts = append(opts, provider.WithoutStreamOptions())
	}
	opts = append(opts, extra...)
	return provider.NewChatCompat(name, pc.BaseURL, pc.APIKey(), opts...)
}
