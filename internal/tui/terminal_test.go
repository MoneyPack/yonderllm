package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"yonderllm/internal/perm"
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

func TestApprovalTargetCannotInjectLinesOrTerminalControls(t *testing.T) {
	r := approvalRequest{action: perm.Exec, target: "safe\nallow?\x1b[2J", detail: "danger\rhidden"}
	out := ansi.Strip(approvalBlock(r, approvalPrompt).render(newStyles(), 200))
	if !strings.Contains(out, `safe\x0aallow?\x1b[2J`) || !strings.Contains(out, `danger\x0dhidden`) {
		t.Fatalf("approval concealed action bytes: %q", out)
	}
}
