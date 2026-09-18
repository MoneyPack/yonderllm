package cli

import (
	"strings"
	"testing"
)

func TestOutputLimitGuidanceAcrossCLIFormats(t *testing.T) {
	for _, args := range [][]string{{"ask", "hello"}, {"run", "hello"}, {"run", "--json", "hello"}} {
		h := newHarness(t, newStubWithFrames(t, []string{contentFrame("partial"), frame(`{"choices":[{"delta":{},"finish_reason":"length"}]}`), frame("[DONE]")}))
		r := h.run(t, args...)
		wantCode(t, r, 0)
		if len(args) == 3 {
			events := decodeNDJSON[wireEvent](t, r.stdout)
			if len(events) != 3 || events[1].Type != "notice" || !strings.Contains(events[1].Notice, "output limit") || events[2].Type != "done" {
				t.Fatal(r.stdout)
			}
		} else if r.stdout != "partial\n" || !strings.Contains(r.stderr, "output limit") {
			t.Fatalf("stdout=%q stderr=%q", r.stdout, r.stderr)
		}
	}
}
