// These tests pin down who owns the keyboard: the prompt while typing, the
// paging keys for the transcript, and a question for exactly as long as it is
// open. Each one is a regression test for a way a keystroke used to reach the
// wrong place.
package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MoneyPack/yonderllm/internal/perm"
)

// longTranscript appends enough blocks that the viewport has somewhere to
// scroll, which is the precondition for any test about scroll position.
func longTranscript(m model, n int) model {
	for i := range n {
		m.append(block{kind: blockUser, text: fmt.Sprintf("line %d of a long conversation", i+1)})
	}
	return m
}

// The viewport binds j/k, u/d, b/f, h/l, space and the arrows to scrolling.
// None of those may move the transcript when typed into the prompt: the
// letters belong in the input, and the arrows belong to its cursor.
func TestTypingDoesNotScrollTheTranscript(t *testing.T) {
	m := longTranscript(newTestModel(t, &stubProvider{name: "stub"}), 40)
	m.view.SetYOffset(5)
	if m.view.YOffset != 5 {
		t.Fatalf("YOffset = %d after SetYOffset(5); the transcript is not long enough to test with", m.view.YOffset)
	}

	const typed = "kujdbfhl "
	for _, r := range typed {
		m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	for _, k := range []tea.KeyType{tea.KeyUp, tea.KeyDown, tea.KeyLeft, tea.KeyRight} {
		m, _ = step(t, m, tea.KeyMsg{Type: k})
	}

	if m.view.YOffset != 5 {
		t.Errorf("YOffset = %d after typing, want 5 — keys reached the viewport", m.view.YOffset)
	}
	if got := m.input.Value(); got != typed {
		t.Errorf("input = %q, want %q — the runes did not all reach the textarea", got, typed)
	}
}

// The paging keys are the transcript's, and stay so: pgup/pgdn page it and
// ctrl+home/ctrl+end jump to either end, none of them touching the prompt.
func TestPagingKeysScrollTheTranscript(t *testing.T) {
	m := longTranscript(newTestModel(t, &stubProvider{name: "stub"}), 40)
	if !m.view.AtBottom() {
		t.Fatal("a fresh transcript is not scrolled to the bottom")
	}
	bottom := m.view.YOffset

	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyPgUp})
	if m.view.YOffset >= bottom {
		t.Errorf("YOffset = %d after pgup, want less than %d", m.view.YOffset, bottom)
	}
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	if !m.view.AtBottom() {
		t.Errorf("YOffset = %d after pgdn, want back at the bottom", m.view.YOffset)
	}

	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyCtrlHome})
	if m.view.YOffset != 0 {
		t.Errorf("YOffset = %d after ctrl+home, want 0", m.view.YOffset)
	}
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyCtrlEnd})
	if !m.view.AtBottom() {
		t.Errorf("YOffset = %d after ctrl+end, want the bottom", m.view.YOffset)
	}

	if got := m.input.Value(); got != "" {
		t.Errorf("input = %q after paging, want it untouched", got)
	}
}

// A quick "y" then "n" used to send two answers, and the second could rewrite
// an allowed call as denied after the tool had already been told yes. The
// first key answers; the second is dropped, not typed and not an answer.
func TestASecondKeyCannotOverturnAnAnswer(t *testing.T) {
	m := newTestModelApprovals(t, &stubProvider{name: "stub"}, perm.Code, NewApprovals())
	m, req, _ := asking(t, m, pendingApproval(perm.Exec, "go", "go test ./..."))

	m, first := step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if first == nil {
		t.Fatal("y did not answer the question")
	}
	if m.asking {
		t.Error("still asking after the answer was sent")
	}

	m, second := step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if second != nil {
		t.Errorf("a second key produced %T, want nothing", second())
	}
	if got := m.input.Value(); got != "" {
		t.Errorf("input = %q, want the second key dropped rather than typed", got)
	}

	// Running the answer command is what delivers the decision. The tool
	// hears yes, and hears it once.
	msg, ok := first().(approvalAnswerMsg)
	if !ok || !msg.allowed {
		t.Fatalf("answer message = %+v, want allowed", msg)
	}
	select {
	case got := <-req.reply:
		if !got {
			t.Error("the tool was told no after y")
		}
	default:
		t.Fatal("the tool was never told anything")
	}
	select {
	case <-req.reply:
		t.Error("the tool was answered twice")
	default:
	}

	m, _ = step(t, m, msg)
	if m.decided != "" {
		t.Errorf("decided = %q after the record arrived, want cleared", m.decided)
	}
	if text := transcript(m); !strings.Contains(text, approvalAllowed) || strings.Contains(text, approvalDenied) {
		t.Errorf("transcript does not record a plain allow:\n%s", text)
	}

	// A second answer message for the same question — the shape the old
	// race produced — changes nothing.
	m, _ = step(t, m, approvalAnswerMsg{request: req, allowed: false})
	if text := transcript(m); strings.Contains(text, approvalDenied) {
		t.Errorf("a duplicate answer overturned the decision:\n%s", text)
	}

	// With the question settled the keyboard is the prompt's again.
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if got := m.input.Value(); got != "x" {
		t.Errorf("input = %q after the question settled, want %q", got, "x")
	}
}

// A question left open when its exchange ends — a timeout, a provider giving
// up — has nobody to act on the answer. The footer used to keep asking and
// swallow every key, ctrl+c included; now the question is closed as denied and
// the keyboard is handed back.
func TestAQuestionDiesWithItsExchange(t *testing.T) {
	m := newTestModelApprovals(t, &stubProvider{name: "stub"}, perm.Code, NewApprovals())
	m = streaming(m, "stub")
	m, req, _ := asking(t, m, pendingApproval(perm.Write, "notes.md", "+ hello"))

	m, _ = step(t, m, streamClosedMsg{seq: 1})

	if m.asking {
		t.Error("still asking after the exchange ended")
	}
	if m.busy {
		t.Error("still busy after the stream closed")
	}
	var found bool
	for _, b := range m.blocks {
		if b.kind == blockApproval && b.id == req.id {
			found = true
			if !strings.Contains(b.text, approvalEnded) {
				t.Errorf("question block does not say the exchange ended:\n%s", b.text)
			}
		}
	}
	if !found {
		t.Fatal("the question's block is gone from the transcript")
	}
	if footer := m.footer(); strings.Contains(footer, "y allow") {
		t.Errorf("footer still asks:\n%s", footer)
	}

	// The tool is told no, in case it is somehow still listening.
	select {
	case got := <-req.reply:
		if got {
			t.Error("the tool was told yes by an exchange that ended")
		}
	default:
		t.Error("the tool was told nothing")
	}

	// ctrl+c is a quit again, not a swallowed deny.
	_, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c after the exchange ended produced no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("ctrl+c produced %T, want tea.QuitMsg", cmd())
	}
}
