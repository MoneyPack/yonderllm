package tui

import (
	"strings"
	"testing"
	"yonderllm/internal/session"
)

func TestActivityTracksToolsAndDiscardsStaleTicks(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m.busy = true
	m.seq = 2
	m.width = 120
	m, _ = step(t, m, streamEventMsg{seq: 2, packet: streamPacket{event: session.Event{Tool: &session.ToolRun{ID: "id", Name: "read"}}}})
	if !strings.Contains(m.footer(), "running read") {
		t.Fatal(m.footer())
	}
	m, _ = step(t, m, streamEventMsg{seq: 2, packet: streamPacket{event: session.Event{Delta: "hi"}}})
	if !strings.Contains(m.footer(), "streaming") {
		t.Fatal(m.footer())
	}
	m, cmd := step(t, m, activityTickMsg{seq: 1})
	if cmd != nil || m.spinner != 0 {
		t.Fatal("stale tick changed state")
	}
	m, cmd = step(t, m, activityTickMsg{seq: 2})
	if cmd == nil || m.spinner != 1 {
		t.Fatal("current tick not animated")
	}
	m.finish()
	m, cmd = step(t, m, activityTickMsg{seq: 2})
	if cmd != nil {
		t.Fatal("idle tick continued")
	}
}
