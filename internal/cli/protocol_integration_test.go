package cli

import (
	"strings"
	"testing"
)

func TestRunProtocolFailuresNeverEmitDone(t *testing.T) {
	cases := map[string][]string{
		"embedded error":  {": heartbeat\n\n", frame(`{"error":{"message":"upstream failure"}}`)},
		"truncated text":  {contentFrame("partial")},
		"malformed frame": {frame(`{bad`)},
		"oversize frame":  {"data: " + strings.Repeat("x", 1024*1024) + "\n\n"},
	}
	for name, frames := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, newStubWithFrames(t, frames))
			r := h.run(t, "run", "--json", "hello")
			wantCode(t, r, 1)
			events := decodeNDJSON[wireEvent](t, r.stdout)
			if len(events) == 0 || events[len(events)-1].Type != "error" {
				t.Fatalf("missing terminal error: %s", r.stdout)
			}
			for _, e := range events {
				if e.Type == "done" {
					t.Fatal("failed stream emitted done")
				}
			}
		})
	}
}
