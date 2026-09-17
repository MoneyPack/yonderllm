// Package config resolves runtime settings from defaults, a TOML file, the
// environment, and command-line flags, in that order of increasing precedence.
//
// API keys are deliberately kept out of the TOML struct's String and error
// output. A key reaches this package only from an environment variable or from
// a config file the user owns, and never leaves it except to an adapter.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
)

// Default values applied before any file, env var, or flag is consulted.
const (
	DefaultProvider  = "groq"
	DefaultMode      = "chat"
	DefaultMaxTokens = 2048
	// DefaultDailyCap bounds requests per day so a free tier is not burned
	// through by an unattended loop. Zero disables the cap.
	DefaultDailyCap = 200
)

// ProviderConfig describes one configured backend.
type ProviderConfig struct {
	// BaseURL overrides the adapter's built-in endpoint. Required for the
	// generic OpenAI-compatible adapter and for local servers.
	BaseURL string `toml:"base_url"`
	// Model is the default model id for this provider.
	Model string `toml:"model"`
	// APIKeyEnv names the environment variable holding the key. Naming the
	// variable rather than the key keeps secrets out of the config file.
	APIKeyEnv         string            `toml:"api_key_env"`
	OmitStreamOptions bool              `toml:"omit_stream_options"`
	HeaderEnv         map[string]string `toml:"header_env"`

	// apiKey is resolved at load time and is never serialized.
	apiKey string
}

// APIKey returns the resolved credential, or the empty string when none was
// found. It is a method rather than a field so that the value cannot be
// written out by a struct dump or TOML round-trip.
func (p ProviderConfig) APIKey() string { return p.apiKey }

// String redacts the credential. Without it, fmt reflects over the unexported
// apiKey field and a %+v dump of a Config — in a log line or a wrapped error —
// would print the key verbatim.
func (p ProviderConfig) String() string {
	key := "unset"
	if p.apiKey != "" {
		key = "[redacted]"
	}
	return fmt.Sprintf("{BaseURL:%s Model:%s APIKeyEnv:%s APIKey:%s}",
		p.BaseURL, p.Model, p.APIKeyEnv, key)
}

// Config is the fully resolved configuration.
type Config struct {
	// Provider is the active provider name.
	Provider string `toml:"provider"`
	// Fallbacks are tried in order when the active provider reports a quota
	// or auth failure.
	Fallbacks []string `toml:"fallbacks"`
	// Mode is the starting permission mode.
	Mode string `toml:"mode"`
	// MaxTokens caps output tokens per request.
	MaxTokens int `toml:"max_tokens"`
	// DailyCap limits requests per day. Zero disables the cap.
	DailyCap              int    `toml:"daily_cap"`
	RetryAttempts         int    `toml:"retry_attempts"`
	RetryBackoffMS        int    `toml:"retry_backoff_ms"`
	RequestTimeoutSeconds int    `toml:"request_timeout_seconds"`
	OutputFormat          string `toml:"output_format"`
	// Providers holds per-provider settings, keyed by provider name.
	Providers map[string]ProviderConfig `toml:"providers"`
}

// defaults returns a Config with the built-in values and the free-tier
// providers registered, before any file or environment is read.
func defaults() Config {
	return Config{
		Provider:       DefaultProvider,
		Fallbacks:      []string{"gemini", "openrouter"},
		Mode:           DefaultMode,
		MaxTokens:      DefaultMaxTokens,
		DailyCap:       DefaultDailyCap,
		RetryBackoffMS: 500,
		OutputFormat:   "text",
		Providers: map[string]ProviderConfig{
			"groq": {
				BaseURL:   "https://api.groq.com/openai/v1",
				Model:     "llama-3.3-70b-versatile",
				APIKeyEnv: "GROQ_API_KEY",
			},
			"gemini": {
				BaseURL:   "https://generativelanguage.googleapis.com/v1beta/openai",
				Model:     "gemini-2.0-flash",
				APIKeyEnv: "GEMINI_API_KEY",
			},
			"openrouter": {
				BaseURL:   "https://openrouter.ai/api/v1",
				Model:     "meta-llama/llama-3.3-70b-instruct:free",
				APIKeyEnv: "OPENROUTER_API_KEY",
			},
			"surplus": {
				BaseURL:   "https://api.surplusintelligence.ai/v1",
				Model:     "gpt-5.6-sol",
				APIKeyEnv: "SURPLUS_API_KEY",
			},
		},
	}
}

// Path returns the location of the config file: $YONDERLLM_CONFIG when set,
// otherwise yonderllm/config.toml under the user's config directory.
func Path() (string, error) {
	if p := os.Getenv("YONDERLLM_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating user config directory: %w", err)
	}
	return filepath.Join(dir, "yonderllm", "config.toml"), nil
}

// Load resolves configuration from defaults, then the file at path if it
// exists, then the environment. A missing file is not an error: yonderllm runs
// on defaults plus an API key env var alone.
//
// Flags are applied by the caller afterwards, so that a flag always wins.
func Load(path string) (Config, error) {
	cfg := defaults()

	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := validateFilePermissions(path); err != nil {
				return Config{}, err
			}
			if err := mergeTOML(&cfg, data); err != nil {
				return Config{}, fmt.Errorf("parsing %s: %w", path, err)
			}
		case os.IsNotExist(err):
			// Running without a config file is a supported setup.
		default:
			return Config{}, fmt.Errorf("reading %s: %w", path, err)
		}
	}

	applyEnv(&cfg)
	resolveKeys(&cfg)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateFilePermissions(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("checking config permissions: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("config file %s is accessible to group or other users; set permissions to 0600", path)
	}
	return nil
}

// mergeTOML decodes data over cfg. Absent keys keep their existing value, and
// per-provider tables merge field by field so that a file overriding only a
// model does not erase the built-in base URL.
func mergeTOML(cfg *Config, data []byte) error {
	var file Config
	md, err := toml.Decode(string(data), &file)
	if err != nil {
		return err
	}

	if file.Provider != "" {
		cfg.Provider = file.Provider
	}
	if file.Fallbacks != nil {
		cfg.Fallbacks = file.Fallbacks
	}
	if file.Mode != "" {
		cfg.Mode = file.Mode
	}
	if md.IsDefined("max_tokens") {
		cfg.MaxTokens = file.MaxTokens
	}
	if md.IsDefined("daily_cap") {
		cfg.DailyCap = file.DailyCap
	}
	if md.IsDefined("retry_attempts") {
		cfg.RetryAttempts = file.RetryAttempts
	}
	if md.IsDefined("retry_backoff_ms") {
		cfg.RetryBackoffMS = file.RetryBackoffMS
	}
	if md.IsDefined("request_timeout_seconds") {
		cfg.RequestTimeoutSeconds = file.RequestTimeoutSeconds
	}
	if md.IsDefined("output_format") {
		cfg.OutputFormat = file.OutputFormat
	}

	for name, fp := range file.Providers {
		base := cfg.Providers[name]
		if md.IsDefined("providers", name, "omit_stream_options") {
			base.OmitStreamOptions = fp.OmitStreamOptions
		}
		if fp.HeaderEnv != nil {
			base.HeaderEnv = fp.HeaderEnv
		}
		if fp.BaseURL != "" {
			base.BaseURL = fp.BaseURL
		}
		if fp.Model != "" {
			base.Model = fp.Model
		}
		if fp.APIKeyEnv != "" {
			base.APIKeyEnv = fp.APIKeyEnv
		}
		cfg.Providers[name] = base
	}
	return nil
}

// applyEnv overlays YONDERLLM_* environment variables onto cfg.
func applyEnv(cfg *Config) {
	if v := os.Getenv("YONDERLLM_PROVIDER"); v != "" {
		cfg.Provider = v
	}
	if v := os.Getenv("YONDERLLM_MODEL"); v != "" {
		p := cfg.Providers[cfg.Provider]
		p.Model = v
		cfg.Providers[cfg.Provider] = p
	}
	if v := os.Getenv("YONDERLLM_MODE"); v != "" {
		cfg.Mode = v
	}
}

// resolveKeys reads each provider's credential from its named environment
// variable. A provider without a key stays configured but unusable; the
// providers subcommand reports which ones lack credentials.
func resolveKeys(cfg *Config) {
	for name, p := range cfg.Providers {
		if p.APIKeyEnv != "" {
			p.apiKey = strings.TrimSpace(os.Getenv(p.APIKeyEnv))
		}
		cfg.Providers[name] = p
	}
}

// Validate reports configuration that cannot produce a working session.
func (c Config) Validate() error {
	if c.Provider == "" {
		return fmt.Errorf("no provider selected")
	}
	if _, ok := c.Providers[c.Provider]; !ok {
		return fmt.Errorf("provider %q is not configured", c.Provider)
	}
	if c.MaxTokens <= 0 {
		return fmt.Errorf("max_tokens must be positive, got %d", c.MaxTokens)
	}
	if c.DailyCap < 0 {
		return fmt.Errorf("daily_cap must not be negative, got %d", c.DailyCap)
	}
	if c.RetryAttempts < 0 || c.RetryAttempts > 3 {
		return fmt.Errorf("retry_attempts must be between 0 and 3")
	}
	if c.RetryBackoffMS < 0 || c.RetryBackoffMS > 30000 {
		return fmt.Errorf("retry_backoff_ms must be between 0 and 30000")
	}
	if c.RequestTimeoutSeconds < 0 || c.RequestTimeoutSeconds > 3600 {
		return fmt.Errorf("request_timeout_seconds must be between 0 and 3600")
	}
	if c.OutputFormat != "" && c.OutputFormat != "text" && c.OutputFormat != "json" {
		return fmt.Errorf("output_format must be text or json")
	}
	for _, name := range c.Fallbacks {
		if _, ok := c.Providers[name]; !ok {
			return fmt.Errorf("fallback provider %q is not configured", name)
		}
	}
	for name, p := range c.Providers {
		for header, env := range p.HeaderEnv {
			if !strings.HasPrefix(strings.ToLower(header), "x-") || strings.ContainsAny(header, " \t\r\n:") || env == "" || strings.ContainsAny(env, "=\r\n ") {
				return fmt.Errorf("provider %q header_env requires X- headers and environment variable names", name)
			}
		}
		if err := validateBaseURL(name, p.BaseURL); err != nil {
			return err
		}
	}
	return nil
}

func validateBaseURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("provider %q has invalid base_url: expected an http or https URL", name)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("provider %q base_url must not contain credentials, query parameters, or fragments", name)
	}
	return nil
}

// Chain returns the active provider followed by its fallbacks, with the active
// provider removed from the fallback positions so it is never tried twice.
func (c Config) Chain() []string {
	chain := []string{c.Provider}
	seen := map[string]bool{c.Provider: true}
	for _, name := range c.Fallbacks {
		if !seen[name] {
			chain = append(chain, name)
			seen[name] = true
		}
	}
	return chain
}

// Credentialed reports whether a provider has a usable key. Providers reached
// over a local server need no key, so an empty APIKeyEnv counts as satisfied.
func (c Config) Credentialed(name string) bool {
	p, ok := c.Providers[name]
	if !ok {
		return false
	}
	return p.APIKeyEnv == "" || p.apiKey != ""
}
