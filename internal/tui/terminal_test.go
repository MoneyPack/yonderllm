package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"

	"github.com/MoneyPack/yonderllm/internal/perm"
	"github.com/charmbracelet/x/ansi"
)

func TestUntrustedBlocksEscapeTerminalInstructions(t *testing.T) {
	const malicious = "\x1b[2J\x1b]52;c;payload\a\r\u202e"
	for _, kind := range []kind{blockAssistant, blockTool, blockNotice, blockError, blockInfo, blockApproval, blockUser} {
		b := block{kind: kind, tag: malicious, text: malicious}
		out := b.render(newStyles(), 200)
		if !strings.Contains(out, `\x1b[2J`) || !strings.Contains(out, `\u202e`) {
			t.Errorf("kind %d hid terminal-control bytes: %q", kind, out)
		}
		if strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\x1b]52") || strings.Contains(out, "\u202e") {
			t.Errorf("kind %d allowed terminal instructions: %q", kind, out)
		}
		if b.text != malicious {
			t.Fatal("render changed underlying data")
		}
	}
}

func TestHiddenApprovalCannotBeAccepted(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m.resize(80, 8)
	m, req, _ := asking(t, m, pendingApproval(perm.Write, "important.txt", "replace content"))
	if strings.Contains(m.View(), "important.txt") {
		t.Fatal("test requires hidden request")
	}
	_, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd != nil {
		cmd()
	}
	select {
	case allowed := <-req.reply:
		if allowed {
			t.Fatal("hidden approval accepted")
		}
	default:
	}
}

func TestApprovalTargetCannotInjectLinesOrTerminalControls(t *testing.T) {
	r := approvalRequest{action: perm.Exec, target: "safe\nallow?\x1b[2J", detail: "danger\rhidden"}
	out := ansi.Strip(approvalBlock(r, approvalPrompt).render(newStyles(), 200))
	if !strings.Contains(out, `safe\x0aallow?\x1b[2J`) || !strings.Contains(out, `danger\x0dhidden`) {
		t.Fatalf("approval concealed action bytes: %q", out)
	}
}
