// Tests for the models command.
//
// The catalogue command is the one read-only path that still talks to a
// backend, so these tests lean on the stub's /models endpoint rather than its
// chat stream. Two properties get the most attention. The first is the free
// tier filter: yonderllm hides paid models by default precisely so nobody
// spends money by accident, and the empty-catalogue wording has to tell
// "nothing at all" apart from "nothing free". The second is the JSON contract,
// which is a published shape and must not drift when an internal field is
// renamed.
package cli

import (
	"strings"
	"testing"
)

// paidCatalogue is the default stub catalogue. The chat-completions /models
// endpoint carries no free-tier signal, so every model it reports is paid as
// far as the adapter is concerned; tests that want table output pass --all.
func TestModelsListsTheCatalogueWithAll(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "models", "--all")

	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout,
		"MODEL", "NAME", "CONTEXT", "TIER",
		"llama-3.1-8b-instant",
		"llama-3.3-70b-versatile",
	)
}

// Without --all the filter removes everything, and the message has to point at
// the flag rather than leaving the user thinking the provider is broken.
func TestModelsHidesPaidModelsByDefault(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "models")

	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, "groq reports no free-tier models. Try --all.")
	wantNotContains(t, "stdout", r.stdout, "llama-3.1-8b-instant", "MODEL")
}

// An empty catalogue under --all is a different situation and says so.
func TestModelsReportsAnEmptyCatalogue(t *testing.T) {
	s := newStub(t)
	s.models = nil
	h := newHarness(t, s)

	r := h.run(t, "models", "--all")

	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, "groq reports no models.")
	wantNotContains(t, "stdout", r.stdout, "Try --all")
}

// The configured model is marked so a stale config is visible at a glance.
func TestModelsMarksTheActiveModel(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "models", "--all")

	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout,
		"* llama-3.1-8b-instant",
		"* active model for groq",
	)
	// The other model must not carry the marker.
	if strings.Contains(r.stdout, "* llama-3.3-70b-versatile") {
		t.Errorf("inactive model is marked active\n--- stdout ---\n%s", r.stdout)
	}
}

// A context window is rendered in K when it divides evenly, and spelled out
// when the provider does not report one, so a bare 0 never reads as a limit.
func TestModelsRendersContextWindows(t *testing.T) {
	s := newStub(t)
	s.models = []stubModel{
		{ID: "big", ContextWindow: 131072},
		{ID: "medium", ContextWindow: 32768},
		{ID: "odd", ContextWindow: 30000},
		{ID: "silent"},
	}
	h := newHarness(t, s)

	r := h.run(t, "models", "--all")

	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, "128K", "32K", "30000", "unknown")
}

// Every model the stub reports is paid, so the tier column says so plainly.
func TestModelsLabelsTheTier(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "models", "--all")

	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, "paid")
}

// A provider that supplies no label leaves the name column filled with the id
// rather than blank.
func TestModelsFallsBackToTheIDForTheName(t *testing.T) {
	s := newStub(t)
	s.models = []stubModel{{ID: "nameless-model", ContextWindow: 4096}}
	h := newHarness(t, s)

	r := h.run(t, "models", "--all")

	wantCode(t, r, 0)
	// The id appears twice on the row: once as the id, once as the name.
	row := findLine(t, r.stdout, "nameless-model")
	if strings.Count(row, "nameless-model") != 2 {
		t.Errorf("name column did not fall back to the id\n--- row ---\n%s", row)
	}
}

// Ordering is by id rather than by whatever order the provider returned, so
// output is diffable between runs.
func TestModelsSortsByID(t *testing.T) {
	s := newStub(t)
	s.models = []stubModel{
		{ID: "zeta", ContextWindow: 4096},
		{ID: "alpha", ContextWindow: 4096},
		{ID: "mu", ContextWindow: 4096},
	}
	h := newHarness(t, s)

	r := h.run(t, "models", "--all")

	wantCode(t, r, 0)
	wantOrder(t, r.stdout, "alpha", "mu", "zeta")
}

// The positional provider is a convenience spelling of --provider, and an
// unknown one fails with the resolver's message rather than a panic.
func TestModelsAcceptsAPositionalProvider(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "models", "groq", "--all")

	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, "llama-3.1-8b-instant")
}

func TestModelsRejectsAnUnknownProvider(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "models", "nope")

	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr, `provider "nope" is not configured`)
}

// A provider with no credential fails before any request is made.
func TestModelsRejectsAnUncredentialedProvider(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "models", "gemini")

	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr, "GEMINI_API_KEY")
}

func TestModelsRejectsTooManyArguments(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "models", "groq", "extra")

	wantCode(t, r, 1)
	wantNotContains(t, "stdout", r.stdout, "MODEL")
}

// The JSON document is a single object, not the newline-delimited stream that
// run --json produces, because a catalogue is a finite answer.
func TestModelsJSON(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "models", "--all", "--json")

	wantCode(t, r, 0)
	doc := decodeJSON[wireCatalogue](t, r.stdout)

	if doc.Provider != "groq" {
		t.Errorf("provider = %q, want %q", doc.Provider, "groq")
	}
	if doc.Active != "llama-3.1-8b-instant" {
		t.Errorf("active_model = %q, want %q", doc.Active, "llama-3.1-8b-instant")
	}
	if len(doc.Models) != 2 {
		t.Fatalf("models length = %d, want 2\n--- stdout ---\n%s", len(doc.Models), r.stdout)
	}

	first := doc.Models[0]
	if first.ID != "llama-3.1-8b-instant" {
		t.Errorf("first id = %q, want %q", first.ID, "llama-3.1-8b-instant")
	}
	if first.Name != "llama-3.1-8b-instant" {
		t.Errorf("first name = %q, want it to fall back to the id", first.Name)
	}
	if first.ContextWindow != 131072 {
		t.Errorf("first context_window = %d, want 131072", first.ContextWindow)
	}
	if first.Free {
		t.Error("first model reported free, want paid")
	}
	if !first.Active {
		t.Error("configured model not marked active in JSON")
	}
	if doc.Models[1].Active {
		t.Error("second model marked active, want only the configured one")
	}
}

// An empty catalogue must serialise as [] rather than null so a consumer can
// range over it unconditionally.
func TestModelsJSONEmptyCatalogueIsAnArray(t *testing.T) {
	s := newStub(t)
	s.models = nil
	h := newHarness(t, s)

	r := h.run(t, "models", "--all", "--json")

	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, `"models": []`)
	wantNotContains(t, "stdout", r.stdout, "null")

	doc := decodeJSON[wireCatalogue](t, r.stdout)
	if doc.Models == nil {
		t.Error("decoded models is nil, want an empty slice")
	}
}

// The filter applies to JSON exactly as it does to the table.
func TestModelsJSONHonoursTheFreeFilter(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "models", "--json")

	wantCode(t, r, 0)
	doc := decodeJSON[wireCatalogue](t, r.stdout)
	if len(doc.Models) != 0 {
		t.Errorf("models length = %d, want 0 without --all", len(doc.Models))
	}
}

// findLine returns the first line containing the fragment, failing the test if
// there is none.
func findLine(t *testing.T, text, fragment string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, fragment) {
			return line
		}
	}
	t.Fatalf("no line contains %q\n--- text ---\n%s", fragment, text)
	return ""
}

// wantOrder fails the test unless the fragments appear in the given order.
func wantOrder(t *testing.T, text string, fragments ...string) {
	t.Helper()
	prev := -1
	for _, f := range fragments {
		at := strings.Index(text, f)
		if at < 0 {
			t.Fatalf("text does not contain %q\n--- text ---\n%s", f, text)
		}
		if at < prev {
			t.Errorf("%q appears out of order\n--- text ---\n%s", f, text)
		}
		prev = at
	}
}
