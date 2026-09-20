package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRetryCommandDrainsFailedExchangeWithoutDuplicatePrompt(t *testing.T) {
	p := &stubProvider{name: "stub", err: errors.New("upstream error")}
	m := newTestModel(t, p)
	drainExchange(t, &m, "original question")
	if !strings.Contains(transcript(m), "/retry") {
		t.Fatal("missing retry hint")
	}
	p.err = nil
	p.deltas = []string{"recovered answer"}
	m = typing(m, "/retry")
	m, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !m.busy {
		t.Fatal("retry did not start readable stream")
	}
	for i := 0; cmd != nil && i < 64; i++ {
		msg := cmd()
		m, cmd = step(t, m, msg)
		if _, ok := msg.(streamClosedMsg); ok {
			break
		}
	}
	if m.busy || !strings.Contains(transcript(m), "recovered answer") {
		t.Fatal("retry did not finish")
	}
	if len(p.last.Messages) != 1 || p.last.Messages[0].Content != "original question" {
		t.Fatalf("retry duplicated input: %+v", p.last.Messages)
	}
	if len(p.last.Tools) != 0 {
		t.Fatal("retry exposed tools")
	}
}

func TestRetryWaitsForWorkerAndRejectsInvalidArguments(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	for _, text := range []string{"/retry", "/retry extra"} {
		m = typing(m, text)
		var cmd tea.Cmd
		m, cmd = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		if cmd != nil || m.busy {
			t.Fatal("invalid retry started exchange")
		}
	}
	done := make(chan struct{})
	m.current.done = done
	m = typing(m, "/retry")
	m, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || !strings.Contains(transcript(m), "still stopping") {
		t.Fatal("retry did not wait for worker")
	}
	close(done)
}

func TestCancelledWorkerBlocksNewInputWithoutDiscardingIt(t *testing.T) {
	for _, input := range []string{"new question", "/clear", "/model another", "/save snapshot"} {
		t.Run(input, func(t *testing.T) {
			m := newTestModel(t, &stubProvider{name: "stub"})
			done := make(chan struct{})
			m.current.done = done
			m = typing(m, input)
			m, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			if cmd != nil || m.busy || m.input.Value() != input || !strings.Contains(transcript(m), "still stopping") {
				t.Errorf("input raced cancelled worker or was lost: busy=%v input=%q", m.busy, m.input.Value())
			}
			close(done)
		})
	}
}
