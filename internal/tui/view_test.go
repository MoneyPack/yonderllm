package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestViewFitsTerminalAfterResize(t *testing.T) {
	for _, state := range []string{"welcome", "typing", "busy", "approval", "error"} {
		t.Run(state, func(t *testing.T) {
			m := newTestModel(t, &stubProvider{name: "stub"})
			switch state {
			case "typing":
				m = typing(m, "界 hello\nsecond line\nthird line")
			case "busy":
				m.busy, m.activity = true, "running 搜索文件"
			case "approval":
				m.asking = true
			case "error":
				m.append(block{kind: blockError, text: "upstream unavailable; use /retry to recover the answer"})
			}
			for _, size := range [][2]int{{100, 30}, {28, 9}, {12, 8}, {20, 4}, {5, 3}, {2, 2}, {1, 1}, {40, 14}, {0, 0}} {
				t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
					m, _ = step(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
					out := m.View()
					if size[0] == 0 || size[1] == 0 {
						if out != "" {
							t.Error("zero-size window rendered content")
						}
						return
					}
					if lipgloss.Width(out) > size[0] || lipgloss.Height(out) > size[1] {
						t.Errorf("frame is %dx%d, exceeds %v:\n%s", lipgloss.Width(out), lipgloss.Height(out), size, out)
					}
					if size[0] >= 20 && !strings.Contains(out, "chat") {
						t.Error("permission mode lost in short window")
					}
				})
			}
		})
	}
}

func TestTruncateUsesTerminalCells(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		width       int
		want        string
	}{
		{"wide", "界界界", 4, "界…"},
		{"combining fits", "e\u0301e\u0301", 2, "e\u0301e\u0301"},
		{"emoji cluster", "👩‍💻abcd", 4, "👩‍💻a…"},
		{"ANSI", "\x1b[31m界界界\x1b[0m", 4, "界…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := truncate(tc.input, tc.width)
			if ansi.Strip(out) != tc.want || lipgloss.Width(out) > tc.width {
				t.Errorf("truncate = %q (%d cells), want %q", out, lipgloss.Width(out), tc.want)
			}
		})
	}
}

func TestNarrowBusyFooterKeepsStopHint(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m.busy, m.activity = true, "running 搜索文件"
	m.resize(28, 9)
	out := m.footer()
	if !strings.Contains(out, "ctrl+c stop") || !strings.Contains(out, "running") || lipgloss.Width(out) > 28 {
		t.Fatalf("narrow activity footer lost phase or stop hint: %s", out)
	}
}

func TestRecoveryBlocksWrapWithoutLosingText(t *testing.T) {
	for _, kind := range []kind{blockNotice, blockError} {
		t.Run(fmt.Sprintf("kind=%d", kind), func(t *testing.T) {
			const text = "provider unavailable; /retry requests an answer with tools disabled"
			out := block{kind: kind, text: text}.render(newStyles(), 20)
			if lipgloss.Width(out) > 20 {
				t.Errorf("recovery message will be clipped by viewport: %q", out)
			}
			compact := strings.Join(strings.Fields(ansi.Strip(out)), "")
			if !strings.Contains(compact, strings.ReplaceAll(text, " ", "")) {
				t.Errorf("recovery text lost while wrapping: %q", out)
			}
		})
	}
}
