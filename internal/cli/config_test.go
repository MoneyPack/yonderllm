// Tests for the `config` command group.
//
// These cover the three things the group promises: that `show` reports the
// configuration after every layer has been folded in, that `path` names the
// file it would read whether or not it exists, and that `init` writes a
// starter file without ever clobbering one by accident. Throughout, the
// standing rule is asserted too: no API key reaches the output, only the name
// of the variable it is read from.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigShowTable(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "config", "show")
	wantCode(t, r, 0)

	wantContains(t, "stdout", r.stdout,
		h.configPath+" (--config)",
		"provider     groq",
		"model        llama-3.1-8b-instant",
		"mode         chat",
		"max tokens   2048",
		"daily cap    200 requests/day",
		"fallbacks    -",
		"PROVIDER",
		"BASE URL",
		"groq",
		"gemini",
		"openrouter",
		testKeyEnv,
		"ready",
		"no key",
	)
}

// The group's central promise: a key that is set is reported as present, and
// its value never appears.
func TestConfigShowHidesKeyValues(t *testing.T) {
	h := newHarness(t, newStub(t))
	t.Setenv(testKeyEnv, "sk-super-secret-value")

	for _, args := range [][]string{
		{"config", "show"},
		{"config", "show", "--json"},
	} {
		r := h.run(t, args...)
		wantCode(t, r, 0)
		wantNotContains(t, "stdout of "+strings.Join(args, " "), r.stdout,
			"sk-super-secret-value")
		wantContains(t, "stdout of "+strings.Join(args, " "), r.stdout, testKeyEnv)
	}
}

// Providers are listed in the same order the providers subcommand uses: the
// active one first, then the rest alphabetically.
func TestConfigShowProviderOrder(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "config", "show")
	wantCode(t, r, 0)

	groq := strings.Index(r.stdout, "\ngroq")
	gemini := strings.Index(r.stdout, "\ngemini")
	openrouter := strings.Index(r.stdout, "\nopenrouter")

	if groq < 0 || gemini < 0 || openrouter < 0 {
		t.Fatalf("not every provider row is present\n--- stdout ---\n%s", r.stdout)
	}
	if !(groq < gemini && gemini < openrouter) {
		t.Errorf("provider rows are out of order: groq=%d gemini=%d openrouter=%d\n--- stdout ---\n%s",
			groq, gemini, openrouter, r.stdout)
	}
}

func TestConfigShowJSON(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "config", "show", "--json")
	wantCode(t, r, 0)

	got := decodeJSON[wireConfig](t, r.stdout)

	if got.ConfigFile != h.configPath {
		t.Errorf("config_file = %q, want %q", got.ConfigFile, h.configPath)
	}
	if got.ConfigSource != "--config" {
		t.Errorf("config_source = %q, want %q", got.ConfigSource, "--config")
	}
	if got.Provider != "groq" {
		t.Errorf("provider = %q, want %q", got.Provider, "groq")
	}
	if got.Model != "llama-3.1-8b-instant" {
		t.Errorf("model = %q, want %q", got.Model, "llama-3.1-8b-instant")
	}
	if got.Mode != "chat" {
		t.Errorf("mode = %q, want %q", got.Mode, "chat")
	}
	if got.MaxTokens != 2048 {
		t.Errorf("max_tokens = %d, want 2048", got.MaxTokens)
	}
	if got.DailyCap != 200 {
		t.Errorf("daily_cap = %d, want 200", got.DailyCap)
	}
	if len(got.Fallbacks) != 0 {
		t.Errorf("fallbacks = %v, want empty", got.Fallbacks)
	}

	// An empty fallback list must encode as [] rather than null, so a consumer
	// can range over it without a nil check.
	wantContains(t, "stdout", r.stdout, `"fallbacks": []`)

	byName := map[string]wireConfigProvider{}
	for _, p := range got.Providers {
		byName[p.Name] = p
	}

	groq, ok := byName["groq"]
	if !ok {
		t.Fatalf("no groq entry in %+v", got.Providers)
	}
	if !groq.Ready {
		t.Error("groq should be ready: its key variable is set")
	}
	if groq.APIKeyEnv != testKeyEnv {
		t.Errorf("groq api_key_env = %q, want %q", groq.APIKeyEnv, testKeyEnv)
	}
	if groq.BaseURL != h.stub.server.URL {
		t.Errorf("groq base_url = %q, want %q", groq.BaseURL, h.stub.server.URL)
	}
	if gem := byName["gemini"]; gem.Ready {
		t.Error("gemini should not be ready: no key is set for it")
	}
}

// Flags beat the file, and `show` must report the resolved value rather than
// what was written on disk.
func TestConfigShowAppliesFlagOverrides(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "--provider", "gemini", "--mode", "code",
		"--max-tokens", "512", "config", "show", "--json")
	wantCode(t, r, 0)

	got := decodeJSON[wireConfig](t, r.stdout)
	if got.Provider != "gemini" {
		t.Errorf("provider = %q, want %q", got.Provider, "gemini")
	}
	if got.Mode != "code" {
		t.Errorf("mode = %q, want %q", got.Mode, "code")
	}
	if got.MaxTokens != 512 {
		t.Errorf("max_tokens = %d, want 512", got.MaxTokens)
	}
}

// Environment variables beat the file but lose to flags.
func TestConfigShowAppliesEnvOverrides(t *testing.T) {
	h := newHarness(t, newStub(t))
	t.Setenv("YONDERLLM_MODE", "agent")
	t.Setenv("YONDERLLM_MODEL", "from-the-environment")

	r := h.run(t, "config", "show", "--json")
	wantCode(t, r, 0)

	got := decodeJSON[wireConfig](t, r.stdout)
	if got.Mode != "agent" {
		t.Errorf("mode = %q, want %q", got.Mode, "agent")
	}
	if got.Model != "from-the-environment" {
		t.Errorf("model = %q, want %q", got.Model, "from-the-environment")
	}
}

// A missing file is not an error: the defaults stand, and the source label
// says so instead of pretending a file was read.
func TestConfigShowWithoutAFile(t *testing.T) {
	scrubEnv(t)
	path := filepath.Join(t.TempDir(), "absent.toml")
	t.Setenv("YONDERLLM_CONFIG", path)

	r := runBare(t, "config", "show")
	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout,
		path,
		"not found, using defaults",
		"provider     groq",
		"fallbacks    gemini, openrouter",
	)
}

func TestConfigPathWithExistingFile(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "config", "path")
	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, h.configPath)
	if strings.TrimSpace(r.stderr) != "" {
		t.Errorf("stderr should be quiet for a file that exists, got:\n%s", r.stderr)
	}
}

// When the file is absent the path is still printed on stdout — so it stays
// usable in a pipeline — and the advisory goes to stderr.
func TestConfigPathWithMissingFile(t *testing.T) {
	scrubEnv(t)
	path := filepath.Join(t.TempDir(), "absent.toml")
	t.Setenv("YONDERLLM_CONFIG", path)

	r := runBare(t, "config", "path")
	wantCode(t, r, 0)

	if strings.TrimSpace(r.stdout) != path {
		t.Errorf("stdout = %q, want just the path %q", r.stdout, path)
	}
	wantContains(t, "stderr", r.stderr,
		"note: this file does not exist yet",
		`run "yonderllm config init" to create it`,
	)
}

func TestConfigInitWritesAStarterFile(t *testing.T) {
	scrubEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("YONDERLLM_CONFIG", path)

	r := runBare(t, "config", "init")
	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, "wrote "+path)

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the written file: %v", err)
	}
	wantContains(t, "written file", string(body),
		"# yonderllm configuration",
		`provider = "groq"`,
		`fallbacks = ["gemini", "openrouter"]`,
		`mode = "chat"`,
		"max_tokens = 2048",
		"daily_cap = 200",
		"[providers.groq]",
		`api_key_env = "GROQ_API_KEY"`,
		"[providers.gemini]",
		"[providers.openrouter]",
	)

	// The file is a place a key may end up, so it must not be world-readable.
	wantOwnerOnly(t, path)
}

// The starter file must be loadable by the very parser that will read it, or
// init has handed the user a broken file.
func TestConfigInitOutputIsLoadable(t *testing.T) {
	scrubEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("YONDERLLM_CONFIG", path)

	wantCode(t, runBare(t, "config", "init"), 0)

	r := runBare(t, "config", "show", "--json")
	wantCode(t, r, 0)

	got := decodeJSON[wireConfig](t, r.stdout)
	if got.Provider != "groq" {
		t.Errorf("provider = %q, want %q", got.Provider, "groq")
	}
	if got.ConfigSource != "YONDERLLM_CONFIG" {
		t.Errorf("config_source = %q, want %q", got.ConfigSource, "YONDERLLM_CONFIG")
	}
	if len(got.Fallbacks) != 2 {
		t.Errorf("fallbacks = %v, want two entries", got.Fallbacks)
	}
}

// init creates any missing parent directory rather than failing on it.
func TestConfigInitCreatesTheDirectory(t *testing.T) {
	scrubEnv(t)
	path := filepath.Join(t.TempDir(), "nested", "deeper", "config.toml")
	t.Setenv("YONDERLLM_CONFIG", path)

	wantCode(t, runBare(t, "config", "init"), 0)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file was not created: %v", err)
	}
}

func TestConfigInitRefusesToClobber(t *testing.T) {
	scrubEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("YONDERLLM_CONFIG", path)

	original := "# hand-written, do not lose me\nprovider = \"openrouter\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("seeding the file: %v", err)
	}

	r := runBare(t, "config", "init")
	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr,
		"already exists; pass --force to overwrite it")

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the file back: %v", err)
	}
	if string(body) != original {
		t.Errorf("the existing file was modified:\n%s", body)
	}
}

func TestConfigInitForceOverwrites(t *testing.T) {
	scrubEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("YONDERLLM_CONFIG", path)

	if err := os.WriteFile(path, []byte("# stale\n"), 0o600); err != nil {
		t.Fatalf("seeding the file: %v", err)
	}

	r := runBare(t, "config", "init", "--force")
	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, "wrote "+path)

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the file back: %v", err)
	}
	wantContains(t, "written file", string(body), "# yonderllm configuration")
	wantNotContains(t, "written file", string(body), "# stale")
}

// --config beats YONDERLLM_CONFIG, so init writes where the flag says.
func TestConfigInitHonoursTheConfigFlag(t *testing.T) {
	scrubEnv(t)
	dir := t.TempDir()
	fromEnv := filepath.Join(dir, "from-env.toml")
	fromFlag := filepath.Join(dir, "from-flag.toml")
	t.Setenv("YONDERLLM_CONFIG", fromEnv)

	r := runBare(t, "--config", fromFlag, "config", "init")
	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, "wrote "+fromFlag)

	if _, err := os.Stat(fromFlag); err != nil {
		t.Errorf("the flag path was not written: %v", err)
	}
	if _, err := os.Stat(fromEnv); err == nil {
		t.Error("the environment path was written even though --config was given")
	}
}

// The bare group prints its own help rather than doing anything surprising.
func TestConfigWithoutASubcommand(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "config")
	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout,
		"Usage",
		"config",
		"show",
		"path",
		"init",
	)
}

// wantOwnerOnly checks that no group or world bits are set. Windows does not
// carry POSIX permissions, so the check is skipped where it is meaningless
// rather than asserted falsely.
func wantOwnerOnly(t *testing.T, path string) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	mode := info.Mode().Perm()
	if mode&0o077 == 0 {
		return
	}
	if os.PathSeparator == '\\' {
		t.Skipf("permission bits are not enforced on this platform (mode %04o)", mode)
	}
	t.Errorf("mode = %04o, want no group or world bits", mode)
}
