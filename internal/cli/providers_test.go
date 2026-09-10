// Tests for the providers command.
//
// The command answers entirely from configuration, so these tests never let it
// reach the stub backend: what is under test is whether the report tells the
// truth about the fallback chain and about which credentials are present. The
// harness config sets an empty fallback list, which means groq is the whole
// chain and the remaining defaults are configured-but-unused — exactly the
// shape the report has to distinguish.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig puts an alternate config alongside the harness one and returns
// its path, for tests that need a chain the default harness does not produce.
func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()

	path := filepath.Join(dir, "alt.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

func TestProvidersTable(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "providers")
	wantCode(t, r, 0)

	wantContains(t, "stdout", r.stdout,
		"PROVIDER", "ROLE", "MODEL", "KEY", "STATUS",
		"groq", "active", "llama-3.1-8b-instant", testKeyEnv, "ready",
	)
}

// The active provider carries a marker so the eye can find the head of the
// chain without counting rows.
func TestProvidersMarksTheActiveProvider(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "providers")
	wantCode(t, r, 0)

	var active string
	for _, line := range strings.Split(r.stdout, "\n") {
		if strings.Contains(line, "groq") && strings.Contains(line, "active") {
			active = line
		}
	}
	if active == "" {
		t.Fatalf("no active row found\n--- stdout ---\n%s", r.stdout)
	}
	if !strings.HasPrefix(active, "*") {
		t.Errorf("active row is not marked with *: %q", active)
	}
}

// Providers outside the fallback chain are still listed, because a configured
// provider that no chain reaches is usually a typo rather than an intention.
func TestProvidersListsUnchainedProviders(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "providers")
	wantCode(t, r, 0)

	wantContains(t, "stdout", r.stdout, "gemini", "openrouter", "configured")
}

// A provider whose key variable is unset reports no key, and the report says
// once how to fix it rather than repeating the advice per row.
func TestProvidersReportsMissingCredentials(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "providers")
	wantCode(t, r, 0)

	wantContains(t, "stdout", r.stdout,
		"no key",
		"GEMINI_API_KEY",
		"OPENROUTER_API_KEY",
		"Set the listed environment variable to enable a provider.",
	)
}

// When every provider is usable the advice line is withheld: there is nothing
// to fix, and a standing instruction would read as a warning.
func TestProvidersOmitsAdviceWhenAllReady(t *testing.T) {
	h := newHarness(t, newStub(t))
	t.Setenv("GEMINI_API_KEY", "gemini-key")
	t.Setenv("OPENROUTER_API_KEY", "openrouter-key")
	t.Setenv("SURPLUS_API_KEY", "surplus-key")

	r := h.run(t, "providers")
	wantCode(t, r, 0)

	wantNotContains(t, "stdout", r.stdout,
		"no key",
		"Set the listed environment variable to enable a provider.",
	)
}

// The report names the variable a provider reads, never the value it holds.
func TestProvidersNeverPrintsAKey(t *testing.T) {
	h := newHarness(t, newStub(t))
	t.Setenv(testKeyEnv, "super-secret-value")

	r := h.run(t, "providers")
	wantCode(t, r, 0)

	wantContains(t, "stdout", r.stdout, testKeyEnv)
	wantNotContains(t, "stdout", r.stdout, "super-secret-value")
}

// Chain order is the order a failing request actually walks, so it drives the
// table rather than alphabetical order.
func TestProvidersOrdersByChain(t *testing.T) {
	h := newHarness(t, newStub(t))
	path := writeConfig(t, h.dir, `provider = "openrouter"
fallbacks = ["gemini", "groq"]
`)

	r := runBare(t, "--config", path, "providers")
	wantCode(t, r, 0)

	openrouter := strings.Index(r.stdout, "openrouter")
	gemini := strings.Index(r.stdout, "gemini")
	groq := strings.Index(r.stdout, "groq")
	if openrouter < 0 || gemini < 0 || groq < 0 {
		t.Fatalf("not every provider was listed\n--- stdout ---\n%s", r.stdout)
	}
	if !(openrouter < gemini && gemini < groq) {
		t.Errorf("rows are not in chain order (openrouter, gemini, groq)\n--- stdout ---\n%s", r.stdout)
	}
}

// The --provider flag re-heads the chain, and the report has to follow it:
// otherwise it would describe a request other than the one that would run.
func TestProvidersFollowsTheProviderFlag(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "--provider", "gemini", "providers")
	wantCode(t, r, 0)

	var active string
	for _, line := range strings.Split(r.stdout, "\n") {
		if strings.Contains(line, "active") && !strings.Contains(line, "ROLE") {
			active = line
		}
	}
	if !strings.Contains(active, "gemini") {
		t.Errorf("active row is %q, want it to name gemini\n--- stdout ---\n%s", active, r.stdout)
	}
}

type wireProviderListDoc struct {
	Providers []struct {
		Name       string `json:"name"`
		Role       string `json:"role"`
		Position   int    `json:"position"`
		Model      string `json:"model"`
		BaseURL    string `json:"base_url"`
		APIKeyEnv  string `json:"api_key_env"`
		Ready      bool   `json:"ready"`
		Credential string `json:"credential"`
	} `json:"providers"`
}

func TestProvidersJSON(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "providers", "--json")
	wantCode(t, r, 0)

	doc := decodeJSON[wireProviderListDoc](t, r.stdout)
	if len(doc.Providers) == 0 {
		t.Fatalf("no providers in JSON output\n--- stdout ---\n%s", r.stdout)
	}

	first := doc.Providers[0]
	if first.Name != "groq" {
		t.Errorf("first provider = %q, want groq", first.Name)
	}
	if first.Role != "active" {
		t.Errorf("first role = %q, want active", first.Role)
	}
	if first.Position != 1 {
		t.Errorf("first position = %d, want 1", first.Position)
	}
	if !first.Ready {
		t.Error("groq is not ready, but its key variable is set")
	}
	if first.Credential != "present" {
		t.Errorf("first credential = %q, want present", first.Credential)
	}
	if first.APIKeyEnv != testKeyEnv {
		t.Errorf("first api_key_env = %q, want %s", first.APIKeyEnv, testKeyEnv)
	}
	if first.BaseURL != h.stub.server.URL {
		t.Errorf("first base_url = %q, want %s", first.BaseURL, h.stub.server.URL)
	}
}

// An uncredentialed provider is reported as missing rather than as merely not
// ready, so a consumer can tell "no key found" from "no key needed".
func TestProvidersJSONMarksMissingCredentials(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "providers", "--json")
	wantCode(t, r, 0)

	doc := decodeJSON[wireProviderListDoc](t, r.stdout)
	for _, p := range doc.Providers {
		if p.Name != "gemini" {
			continue
		}
		if p.Ready {
			t.Error("gemini is ready, but GEMINI_API_KEY is unset")
		}
		if p.Credential != "missing" {
			t.Errorf("gemini credential = %q, want missing", p.Credential)
		}
		if p.Role != "configured" {
			t.Errorf("gemini role = %q, want configured", p.Role)
		}
		if p.Position != 0 {
			t.Errorf("gemini position = %d, want 0", p.Position)
		}
		return
	}
	t.Fatalf("gemini is absent from the JSON output\n--- stdout ---\n%s", r.stdout)
}

// A provider that names no key variable needs none, which is a third state
// distinct from present and missing.
func TestProvidersJSONMarksCredentialNotRequired(t *testing.T) {
	h := newHarness(t, newStub(t))
	path := writeConfig(t, h.dir, `provider = "local"
fallbacks = []

[providers.local]
base_url = "http://127.0.0.1:11434/v1"
model = "llama3"
api_key_env = ""
`)

	r := runBare(t, "--config", path, "providers", "--json")
	wantCode(t, r, 0)

	doc := decodeJSON[wireProviderListDoc](t, r.stdout)
	for _, p := range doc.Providers {
		if p.Name != "local" {
			continue
		}
		if p.Credential != "not required" {
			t.Errorf("local credential = %q, want not required", p.Credential)
		}
		if !p.Ready {
			t.Error("local is not ready, but it needs no credential")
		}
		return
	}
	t.Fatalf("local is absent from the JSON output\n--- stdout ---\n%s", r.stdout)
}

// A provider needing no key says so in the table too, rather than leaving the
// column blank as if the field were missing.
func TestProvidersTableLabelsKeylessProviders(t *testing.T) {
	h := newHarness(t, newStub(t))
	path := writeConfig(t, h.dir, `provider = "local"
fallbacks = []

[providers.local]
base_url = "http://127.0.0.1:11434/v1"
model = "llama3"
api_key_env = ""
`)

	r := runBare(t, "--config", path, "providers")
	wantCode(t, r, 0)

	wantContains(t, "stdout", r.stdout, "local", "none needed", "ready")
}

// providers takes no positional arguments; accepting one silently would hide a
// typo such as "yonderllm providers gemini".
func TestProvidersRejectsArguments(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "providers", "gemini")
	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr, "yonderllm:")
}

// The command answers from configuration alone, so it must not contact a
// backend even when one is reachable.
func TestProvidersDoesNotCallTheBackend(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "providers")
	wantCode(t, r, 0)

	if h.stub.seen {
		t.Error("providers sent a request to the backend")
	}
}
