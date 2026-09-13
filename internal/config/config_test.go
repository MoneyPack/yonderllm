package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeConfig puts a TOML file in a temp dir and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

func TestLoadDefaultsWithoutFile(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("missing config file should not be an error: %v", err)
	}
	if cfg.Provider != DefaultProvider {
		t.Errorf("provider: got %q, want %q", cfg.Provider, DefaultProvider)
	}
	if cfg.Mode != DefaultMode {
		t.Errorf("mode: got %q, want %q", cfg.Mode, DefaultMode)
	}
	if cfg.MaxTokens != DefaultMaxTokens {
		t.Errorf("max_tokens: got %d, want %d", cfg.MaxTokens, DefaultMaxTokens)
	}
}

// TestBuiltInProvidersAreConfigured pins the catalogue that ships with the
// binary. Every provider carries a base URL and the name of its key variable;
// a default model is deliberately left empty except for surplus, whose model
// ids are not discoverable without a credential.
func TestBuiltInProvidersAreConfigured(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := map[string]ProviderConfig{
		"groq": {
			BaseURL:   "https://api.groq.com/openai/v1",
			APIKeyEnv: "GROQ_API_KEY",
		},
		"gemini": {
			BaseURL:   "https://generativelanguage.googleapis.com/v1beta/openai",
			APIKeyEnv: "GEMINI_API_KEY",
		},
		"openrouter": {
			BaseURL:   "https://openrouter.ai/api/v1",
			APIKeyEnv: "OPENROUTER_API_KEY",
		},
		"surplus": {
			BaseURL:   "https://api.surplusintelligence.ai/v1",
			Model:     "gpt-5.6-sol",
			APIKeyEnv: "SURPLUS_API_KEY",
		},
	}

	if len(cfg.Providers) != len(want) {
		t.Errorf("provider count: got %d, want %d", len(cfg.Providers), len(want))
	}
	for name, w := range want {
		got, ok := cfg.Providers[name]
		if !ok {
			t.Errorf("%s: not present in the built-in catalogue", name)
			continue
		}
		if got.BaseURL != w.BaseURL {
			t.Errorf("%s base_url: got %q, want %q", name, got.BaseURL, w.BaseURL)
		}
		if got.Model != w.Model {
			t.Errorf("%s model: got %q, want %q", name, got.Model, w.Model)
		}
		if got.APIKeyEnv != w.APIKeyEnv {
			t.Errorf("%s api_key_env: got %q, want %q", name, got.APIKeyEnv, w.APIKeyEnv)
		}
	}
}

// TestSurplusKeyResolvesFromEnv is the surplus counterpart to the groq case:
// naming SURPLUS_API_KEY is enough for the provider to count as usable, and
// the key itself is never written back into the struct's exported fields.
func TestSurplusKeyResolvesFromEnv(t *testing.T) {
	t.Setenv("SURPLUS_API_KEY", "surplus-test-key")

	cfg, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Credentialed("surplus") {
		t.Error("surplus should be credentialed once SURPLUS_API_KEY is set")
	}
	surplus, ok := cfg.Providers["surplus"]
	if !ok {
		t.Fatal("surplus missing from the built-in catalogue")
	}
	if key := surplus.APIKey(); key != "surplus-test-key" {
		t.Errorf("APIKey(): got %q, want the value from the environment", key)
	}
}

// TestPartialProviderTableMerges is the case that motivates field-by-field
// merging: setting only a model must not wipe the built-in base URL.
func TestPartialProviderTableMerges(t *testing.T) {
	path := writeConfig(t, `
[providers.groq]
model = "llama-3.1-8b-instant"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	groq := cfg.Providers["groq"]
	if groq.Model != "llama-3.1-8b-instant" {
		t.Errorf("model: got %q, want %q", groq.Model, "llama-3.1-8b-instant")
	}
	if groq.BaseURL != "https://api.groq.com/openai/v1" {
		t.Errorf("base_url was clobbered by a partial table: got %q", groq.BaseURL)
	}
	if groq.APIKeyEnv != "GROQ_API_KEY" {
		t.Errorf("api_key_env was clobbered by a partial table: got %q", groq.APIKeyEnv)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	path := writeConfig(t, `provider = "gemini"`)
	t.Setenv("YONDERLLM_PROVIDER", "openrouter")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider != "openrouter" {
		t.Errorf("env should beat file: got %q, want %q", cfg.Provider, "openrouter")
	}
}

func TestAPIKeyResolvedFromNamedEnvVar(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "  test-key  ")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Providers["groq"].APIKey(); got != "test-key" {
		t.Errorf("APIKey: got %q, want %q (surrounding space must be trimmed)", got, "test-key")
	}
	if !cfg.Credentialed("groq") {
		t.Error("groq should be credentialed once its env var is set")
	}
	if cfg.Credentialed("gemini") {
		t.Error("gemini should not be credentialed with no key set")
	}
}

// TestAPIKeyNotSerialized guards the rule that a key never lands in output.
func TestAPIKeyNotSerialized(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "super-secret-value")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Providers["groq"].APIKey(); got != "super-secret-value" {
		t.Fatalf("test setup: key not resolved, got %q", got)
	}

	// A %+v dump is the most likely accidental leak path.
	if dumped := fmt.Sprintf("%+v", cfg); strings.Contains(dumped, "super-secret-value") {
		t.Error("API key appeared in a verbose dump of Config")
	}
}

func TestChainSkipsActiveProvider(t *testing.T) {
	path := writeConfig(t, `
provider = "gemini"
fallbacks = ["groq", "gemini", "openrouter"]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := cfg.Chain()
	want := []string{"gemini", "groq", "openrouter"}
	if !slices.Equal(got, want) {
		t.Errorf("Chain: got %v, want %v", got, want)
	}
}

func TestValidateRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"unknown provider", `provider = "nonesuch"`},
		{"unknown fallback", `fallbacks = ["nonesuch"]`},
		{"negative max_tokens", `max_tokens = -1`},
		{"negative daily_cap", `daily_cap = -5`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, c.body)); err == nil {
				t.Error("got nil error, want validation failure")
			}
		})
	}
}

func TestMalformedTOMLReportsPath(t *testing.T) {
	path := writeConfig(t, `provider = "unterminated`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("got nil error, want parse failure")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error should name the offending file %q, got: %v", path, err)
	}
}

// TestEnvModelOverridesFile covers the YONDERLLM_MODEL branch of applyEnv,
// which the provider-only override test leaves untouched.
func TestEnvModelOverridesFile(t *testing.T) {
	path := writeConfig(t, `
[providers.groq]
model = "from-file"
`)
	t.Setenv("YONDERLLM_MODEL", "from-env")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Providers["groq"].Model; got != "from-env" {
		t.Errorf("model: got %q, want %q", got, "from-env")
	}
}

// TestEnvModelAttachesToOverriddenProvider pins the ordering inside applyEnv:
// the provider override lands first, so the model must follow it rather than
// settle on whichever provider the file named.
func TestEnvModelAttachesToOverriddenProvider(t *testing.T) {
	path := writeConfig(t, `provider = "groq"`)
	t.Setenv("YONDERLLM_PROVIDER", "openrouter")
	t.Setenv("YONDERLLM_MODEL", "some/model:free")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Providers["openrouter"].Model; got != "some/model:free" {
		t.Errorf("model should attach to the overridden provider: got %q", got)
	}
	if got := cfg.Providers["groq"].Model; got == "some/model:free" {
		t.Error("model leaked onto the provider named by the file")
	}
}

func TestEnvModeOverridesFile(t *testing.T) {
	path := writeConfig(t, `mode = "chat"`)
	t.Setenv("YONDERLLM_MODE", "agent")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Mode != "agent" {
		t.Errorf("mode: got %q, want %q", cfg.Mode, "agent")
	}
}

// TestEmptyEnvVarsAreIgnored guards against an exported-but-blank variable
// silently blanking a setting the file established.
func TestEmptyEnvVarsAreIgnored(t *testing.T) {
	path := writeConfig(t, `
provider = "gemini"
mode = "code"
`)
	t.Setenv("YONDERLLM_PROVIDER", "")
	t.Setenv("YONDERLLM_MODEL", "")
	t.Setenv("YONDERLLM_MODE", "")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider != "gemini" {
		t.Errorf("provider: got %q, want %q", cfg.Provider, "gemini")
	}
	if cfg.Mode != "code" {
		t.Errorf("mode: got %q, want %q", cfg.Mode, "code")
	}
}

func TestPathPrefersEnvOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "elsewhere.toml")
	t.Setenv("YONDERLLM_CONFIG", want)

	got, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if got != want {
		t.Errorf("Path: got %q, want %q", got, want)
	}
}

func TestPathFallsBackToUserConfigDir(t *testing.T) {
	t.Setenv("YONDERLLM_CONFIG", "")

	got, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	want := filepath.Join("yonderllm", "config.toml")
	if !strings.HasSuffix(got, want) {
		t.Errorf("Path: got %q, want a path ending in %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("Path: got %q, want an absolute path", got)
	}
}

func TestCredentialedRejectsUnknownProvider(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Credentialed("nonesuch") {
		t.Error("an unconfigured provider must never report as credentialed")
	}
}

// TestCredentialedWithoutEnvVarNeedsNoKey covers the local-server case: a
// provider that names no environment variable is usable as-is.
func TestCredentialedWithoutEnvVarNeedsNoKey(t *testing.T) {
	cfg := Config{Providers: map[string]ProviderConfig{
		"local": {BaseURL: "http://127.0.0.1:8080/v1"},
	}}
	if !cfg.Credentialed("local") {
		t.Error("a provider with no api_key_env should count as credentialed")
	}
}
