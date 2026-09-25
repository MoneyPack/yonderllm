package session

import (
	"testing"

	"github.com/MoneyPack/yonderllm/internal/provider"
)

func TestTurnsCannotMutateToolCalls(t *testing.T) {
	h := History{}
	h.AppendToolCalls("", []provider.ToolCall{{ID: "one", Name: "read"}})
	turns := h.Turns()
	turns[0].ToolCalls[0].Name = "write"
	if h.Turns()[0].ToolCalls[0].Name != "read" {
		t.Fatal("caller changed history")
	}
}
