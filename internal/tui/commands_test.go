// These tests cover the slash commands that leave the message loop to do their
// work: what the transcript shows while they run, what the keyboard does, and
// how the result finds its way back.
package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MoneyPack/yonderllm/internal/perm"
)

// While a command works, the transcript shows a placeholder and Enter is held,
// so that nothing can land around the result in an order nobody chose. Typing
// carries on, since the prompt is not what is busy.
func TestEnterWaitsWhileACommandRuns(t *testing.T) {
	workspaceDir(t, map[string]string{"notes.txt": "alpha\n"})

	m := newTestModelMode(t, &stubProvider{name: "stub"}, perm.Code)
	m = typing(m, "/read notes.txt")
	m, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/read returned no command")
	}
	if m.command != "/read" {
		t.Errorf("command = %q while /read runs, want %q", m.command, "/read")
	}

	at := len(m.blocks) - 1
	placeholder := m.blocks[at]
	if placeholder.kind != blockNotice || !strings.Contains(placeholder.text, "reading notes.txt") {
		t.Errorf("last block = %+v, want a notice that the read is under way", placeholder)
	}
	if footer := m.footer(); !strings.Contains(footer, "running /read") {
		t.Errorf("footer does not say a command is running:\n%s", footer)
	}

	// Typing still reaches the prompt; Enter does not send.
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("next")})
	m, refused := step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if refused != nil {
		t.Errorf("enter during a command produced %T, want nothing", refused())
	}
	if got := m.input.Value(); got != "next" {
		t.Errorf("input = %q after a refused send, want it kept", got)
	}
	if !strings.Contains(transcript(m), "still running /read") {
		t.Errorf("no notice explaining the held Enter:\n%s", transcript(m))
	}
	if m.busy {
		t.Error("a refused send started an exchange")
	}

	// The result takes the placeholder's place, not a new line under it.
	before := len(m.blocks)
	m, _ = step(t, m, cmd())
	if m.command != "" {
		t.Errorf("command = %q after the result arrived, want empty", m.command)
	}
	if len(m.blocks) != before {
		t.Errorf("blocks = %d after the result, want %d — it was appended rather than written in", len(m.blocks), before)
	}
	result := m.blocks[at]
	if result.kind != blockInfo || !strings.Contains(result.text, "alpha") {
		t.Errorf("block %d = %+v, want the file contents in the placeholder's place", at, result)
	}
	if strings.Contains(transcript(m), "reading notes.txt…") {
		t.Errorf("the placeholder outlived the result:\n%s", transcript(m))
	}

	// With the command finished, Enter sends again.
	m, cmd = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !m.busy {
		t.Error("enter after the command finished did not submit the prompt")
	}
	m.cancel()
}

// The mode a command runs under is the mode it was typed in. It is captured
// when the command starts rather than read when it runs, so the block it
// builds does not depend on what happened to the model in between.
func TestCommandsCaptureTheirMode(t *testing.T) {
	workspaceDir(t, map[string]string{"notes.txt": "alpha\n"})

	if b := readBlock(perm.Chat, "notes.txt"); b.kind != blockError || !strings.Contains(b.text, "not permitted") {
		t.Errorf("readBlock in chat mode = %+v, want a denial", b)
	}
	if b := readBlock(perm.Code, "notes.txt"); b.kind != blockInfo || !strings.Contains(b.text, "alpha") {
		t.Errorf("readBlock in code mode = %+v, want the file", b)
	}
	if b := searchBlock(perm.Chat, "alpha"); b.kind != blockError {
		t.Errorf("searchBlock in chat mode = %+v, want a denial", b)
	}
	if b := searchBlock(perm.Code, "alpha"); b.kind != blockInfo || !strings.Contains(b.text, "notes.txt:1: alpha") {
		t.Errorf("searchBlock in code mode = %+v, want the match", b)
	}
}
