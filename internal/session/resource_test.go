package session

import (
	"strings"
	"testing"
	"yonderllm/internal/provider"
)

func TestPlainRedactionDoesNotAllocate(t *testing.T) {
	text := strings.Repeat("ordinary prose ", 100)
	if n := testing.AllocsPerRun(100, func() {
		if redactable(text) != text {
			panic("changed text")
		}
	}); n != 0 {
		t.Fatalf("plain text allocated %g times", n)
	}
}

func TestTurnsCannotMutateToolCalls(t *testing.T) {
	h := History{}
	h.AppendToolCalls("", []provider.ToolCall{{ID: "one", Name: "read"}})
	turns := h.Turns()
	turns[0].ToolCalls[0].Name = "write"
	if h.Turns()[0].ToolCalls[0].Name != "read" {
		t.Fatal("caller changed history")
	}
}
