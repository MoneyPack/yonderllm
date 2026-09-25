// These tests cover how the transcript reaches the viewport: what is cached,
// what is redrawn per token, and where the reader is left when new text
// arrives. The benchmark at the end is the reason the cache exists.
package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/MoneyPack/yonderllm/internal/perm"
	"github.com/MoneyPack/yonderllm/internal/provider"
	"github.com/MoneyPack/yonderllm/internal/session"
)

// Someone who has scrolled up to reread an earlier answer must not be dragged
// back to the bottom by every token of the reply still arriving. Someone who is
// at the bottom, though, is following the reply, and stays there as it grows.
func TestStreamingKeepsTheReadersPlace(t *testing.T) {
	m := longTranscript(newTestModel(t, &stubProvider{name: "stub"}), 40)
	m = streaming(m, "stub")

	m.view.GotoTop()
	if m.view.YOffset != 0 {
		t.Fatalf("YOffset = %d after GotoTop, want 0", m.view.YOffset)
	}
	for range 20 {
		m, _ = step(t, m, streamEventMsg{seq: 1, packet: streamPacket{event: session.Event{Delta: "more words\n", Provider: "stub"}}})
	}
	if m.view.YOffset != 0 {
		t.Errorf("YOffset = %d while streaming, want 0 — the view was yanked to the bottom", m.view.YOffset)
	}

	m.view.GotoBottom()
	for range 20 {
		m, _ = step(t, m, streamEventMsg{seq: 1, packet: streamPacket{event: session.Event{Delta: "and more\n", Provider: "stub"}}})
	}
	if !m.view.AtBottom() {
		t.Errorf("YOffset = %d after streaming from the bottom, want still at the bottom", m.view.YOffset)
	}
	if !strings.Contains(m.view.View(), "and more") {
		t.Errorf("the view at the bottom does not show the newest text:\n%s", m.view.View())
	}
}

// The cache holds committed blocks as drawn at one width. A rewrite in place
// has to reach it, and a resize has to empty it, or the screen would show a
// tool call's announcement after its result had arrived, or text wrapped for a
// window that no longer exists.
func TestRenderCacheFollowsRewritesAndResizes(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m = streaming(m, "stub")

	start := &session.ToolRun{ID: "call-1", Name: "read_file", Arguments: `{"path": "main.go"}`}
	m, _ = step(t, m, streamEventMsg{seq: 1, packet: streamPacket{event: session.Event{Provider: "stub", Tool: start}}})
	if joined := strings.Join(m.rendered, "\n"); !strings.Contains(joined, "(no output)") {
		t.Fatalf("cache does not hold the announced call:\n%s", joined)
	}

	done := &session.ToolRun{ID: "call-1", Name: "read_file", Arguments: `{"path": "main.go"}`, Finished: true, Result: "package main"}
	m, _ = step(t, m, streamEventMsg{seq: 1, packet: streamPacket{event: session.Event{Provider: "stub", Tool: done}}})
	joined := strings.Join(m.rendered, "\n")
	if !strings.Contains(joined, "package main") {
		t.Errorf("cache does not hold the result:\n%s", joined)
	}
	if strings.Contains(joined, "(no output)") {
		t.Errorf("cache still holds the announcement the result replaced:\n%s", joined)
	}
	if len(m.rendered) != len(m.blocks) {
		t.Errorf("cache holds %d blocks, transcript has %d", len(m.rendered), len(m.blocks))
	}

	m, _ = step(t, m, tea.WindowSizeMsg{Width: 30, Height: 20})
	if m.renderedWidth != 30 {
		t.Errorf("renderedWidth = %d after resizing to 30, want 30", m.renderedWidth)
	}
	for i, part := range m.rendered {
		if w := lipgloss.Width(part); w > 30 {
			t.Errorf("cached block %d is %d cells wide after a resize to 30", i, w)
		}
	}
}

// The welcome is chosen when the transcript is drawn, not when the frame is
// read, and leaving it must scroll the first real block into view even though
// the welcome pinned the viewport to the top.
func TestWelcomeIsDrawnByRefreshAndYieldsToTheFirstBlock(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	if !m.welcomed {
		t.Fatal("a fresh model did not draw the welcome")
	}
	if !strings.Contains(m.view.View(), "Your model lives yonder.") {
		t.Fatalf("the viewport does not hold the welcome:\n%s", m.view.View())
	}

	m = longTranscript(m, 40)
	if m.welcomed {
		t.Error("still marked as showing the welcome after the transcript grew")
	}
	if !m.view.AtBottom() {
		t.Errorf("YOffset = %d after the first blocks, want the bottom", m.view.YOffset)
	}
	if strings.Contains(m.view.View(), "Your model lives yonder.") {
		t.Errorf("the welcome survived the first block:\n%s", m.view.View())
	}
}

// Cancelling and then hearing the stream close used to leave two notices —
// "cancelled" and, under it, the retry offer. The offer now lands on the
// cancelled line, and the footer's "stopping" hint comes and goes with the
// worker without View having to ask a channel.
func TestCancelAndCloseLeaveOneNotice(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub", deltas: []string{"partial"}})
	m = typing(m, "a question")
	m, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("submitting returned no command to drain")
	}

	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.unwinding {
		t.Error("not marked as unwinding straight after cancelling")
	}
	if footer := m.footer(); !strings.Contains(footer, "stopping") {
		t.Errorf("footer does not say the worker is stopping:\n%s", footer)
	}

	// Bubble Tea would keep running the reader; here that loop is run by
	// hand until the cancelled stream reports itself closed.
	closed := false
	for i := 0; cmd != nil && i < 64; i++ {
		msg := cmd()
		m, cmd = step(t, m, msg)
		if _, closed = msg.(streamClosedMsg); closed {
			break
		}
	}
	if !closed {
		t.Fatal("the cancelled stream never reported itself closed")
	}
	if m.unwinding {
		t.Error("still marked as unwinding after the stream closed")
	}
	if footer := m.footer(); strings.Contains(footer, "stopping") {
		t.Errorf("footer still says stopping after the worker stopped:\n%s", footer)
	}

	var cancelled int
	for _, b := range m.blocks {
		if b.kind != blockNotice {
			continue
		}
		if strings.Contains(b.text, "answer interrupted") {
			t.Errorf("a separate interruption notice followed the cancellation:\n%s", transcript(m))
		}
		if strings.HasPrefix(b.text, "cancelled") {
			cancelled++
			if m.sess.CanRetry() == nil && !strings.Contains(b.text, retryHint) {
				t.Errorf("the retry offer was not folded into the cancelled line:\n%s", b.text)
			}
		}
	}
	if cancelled != 1 {
		t.Errorf("found %d cancelled notices, want exactly 1:\n%s", cancelled, transcript(m))
	}
}

// BenchmarkStreamDelta measures one streamed token landing on a long
// transcript, which is the hot path the render cache exists for: before it,
// every block was re-wrapped per token.
func BenchmarkStreamDelta(b *testing.B) {
	sess := session.New(testConfig(), func(string) (provider.Provider, error) {
		return &stubProvider{name: "stub"}, nil
	})
	m := newModel(sess, perm.Chat, nil)
	m.resize(100, 40)
	for i := range 200 {
		m.append(block{kind: blockAssistant, tag: "stub", text: fmt.Sprintf("answer %d: %s", i, strings.Repeat("lorem ipsum dolor sit amet ", 12))})
	}
	m = streaming(m, "stub")
	delta := streamEventMsg{seq: 1, packet: streamPacket{event: session.Event{Delta: "token ", Provider: "stub"}}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		next, _ := m.Update(delta)
		m = next.(model)
		if len(m.pending) > 4096 {
			// A reply does not grow without bound, and a pending
			// string that did would measure string growth rather than
			// the render.
			m.pending = ""
		}
	}
}
