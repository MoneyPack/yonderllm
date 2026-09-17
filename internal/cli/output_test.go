package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type brokenOutput struct{}

func (brokenOutput) Write([]byte) (int, error) { return 0, errors.New("output closed") }

func TestBrokenOutputFailsInsteadOfReportingSuccess(t *testing.T) {
	for _, args := range [][]string{{"ask", "hi"}, {"run", "hi"}, {"run", "--json", "hi"}} {
		h := newHarness(t, newStub(t, "first", "second"))
		var stderr bytes.Buffer
		code := Execute(append([]string{"--config", h.configPath}, args...), strings.NewReader(""), brokenOutput{}, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "output closed") {
			t.Fatalf("%v: code=%d stderr=%s", args, code, stderr.String())
		}
	}
}

func TestJSONCarriesSchemaVersion(t *testing.T) {
	h := newHarness(t, newStub(t, "hello"))
	r := h.run(t, "run", "--json", "hi")
	for _, e := range decodeNDJSON[map[string]any](t, r.stdout) {
		if e["schema_version"] != float64(1) {
			t.Fatalf("missing version: %+v", e)
		}
	}
}

func TestPromptInputHasABoundedSize(t *testing.T) {
	large := strings.Repeat("x", 4*1024*1024+1)
	if _, err := readPrompt(strings.NewReader(large), nil); err == nil {
		t.Fatal("unbounded stdin accepted")
	}
	if _, err := readPrompt(strings.NewReader(""), []string{large}); err == nil {
		t.Fatal("unbounded argv accepted")
	}
}

func TestExplicitStdinAppendsContextToInstruction(t *testing.T) {
	for _, command := range []string{"ask", "run"} {
		h := newHarness(t, newStub(t, "ok"))
		r := h.runWithInput(t, "diff context\n", command, "--stdin", "explain this")
		wantCode(t, r, 0)
		wantLastUserMessage(t, h, "explain this\n\ndiff context")
	}
}
