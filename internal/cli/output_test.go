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
