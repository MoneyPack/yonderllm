// These tests cover the edges the main suite leaves open: the branches that
// only run when a channel closes, a cap is set, a width collapses, or a key
// arrives at a moment nothing is happening. Each one is a path the interface
// takes when something has already gone sideways, which is exactly when a
// silent regression would be hardest to notice.
package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"yonderllm/internal/perm"
	"yonderllm/internal/provider"
	"yonderllm/internal/session"
)

// A closed approval channel means there is no interface left to ask. The
// waiting command must report that by returning a nil message rather than
// blocking forever or handing the loop a zero-valued question that would draw
// an empty prompt on screen.
func TestWaitForApprovalOnAClosedChannelYieldsNothing(t *testing.T) {
	a := NewApprovals()
	close(a.ch)

	cmd := waitForApproval(a)
	if cmd == nil {
		t.Fatal("waitForApproval returned no command for a non-nil bridge")
	}
	if msg := cmd(); msg != nil {
		t.Errorf("message from a closed channel = %#v, want nil", msg)
	}
}

// truncate is what keeps the footer on one line. Its boundaries matter more
// than its middle: a width that cannot hold even the ellipsis must yield
// nothing rather than a lone "…", and a string that already fits must come
// back untouched rather than losing its last rune to an off-by-one.
func TestTruncateBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{"zero width", "hello", 0, ""},
		{"negative width", "hello", -3, ""},
		{"width one cannot hold an ellipsis", "hello", 1, ""},
		{"width two is the shortest cut", "hello", 2, "h…"},
		{"exactly fits", "hello", 5, "hello"},
		{"room to spare", "hello", 9, "hello"},
		{"cut with an ellipsis", "hello world", 8, "hello w…"},
		{"empty string", "", 4, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := truncate(c.s, c.width); got != c.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", c.s, c.width, got, c.want)
			}
		})
	}
}

// truncate counts runes, not bytes, so a multi-byte string must be cut on a
// character boundary. Slicing the underlying bytes would produce a replacement
// character in the terminal instead of a clean cut.
func TestTruncateCutsOnRuneBoundaries(t *testing.T) {
	const s = "héllo wörld"
	got := truncate(s, 4)
	if want := "hél…"; got != want {
		t.Errorf("truncate(%q, 4) = %q, want %q", s, got, want)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated text %q does not end in an ellipsis", got)
	}
}

// rule spans the window, and a window with no width yet — the moment before the
// first resize — must still produce a line rather than an empty string or a
// panic from a negative repeat count.
func TestRuleClampsToAtLeastOneCell(t *testing.T) {
	var m model
	m.styles = newStyles()

	for _, width := range []int{0, -1, -80} {
		m.width = width
		got := m.rule()
		if n := strings.Count(got, "─"); n != 1 {
			t.Errorf("rule at width %d drew %d cells, want 1", width, n)
		}
	}

	m.width = 12
	if n := strings.Count(m.rule(), "─"); n != 12 {
		t.Errorf("rule at width 12 drew %d cells, want 12", n)
	}
}

// Cancelling is only meaningful while a reply is in flight. When nothing is
// streaming it must be a no-op: appending a "cancelled" notice to an idle
// transcript would tell the user something was stopped when nothing was.
func TestCancelWhileIdleChangesNothing(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	before := len(m.blocks)

	m.cancel()

	if len(m.blocks) != before {
		t.Errorf("idle cancel added %d blocks, want none", len(m.blocks)-before)
	}
	if m.busy {
		t.Error("idle cancel left the model busy")
	}
	if strings.Contains(transcript(m), "cancelled") {
		t.Errorf("idle cancel wrote a notice:\n%s", transcript(m))
	}
}

// The usage report has a branch for a cap and a branch for a provider
// breakdown, and the suite covers each alone. This covers them together, which
// is the shape a real capped session reaches after its first request: the cap
// line, the remaining count and the per-provider rows all at once.
func TestUsageReportShowsCapAndProviderBreakdown(t *testing.T) {
	cfg := testConfig()
	cfg.DailyCap = 20
	sess := session.New(cfg, func(string) (provider.Provider, error) {
		return &stubProvider{name: "stub", deltas: []string{"hi"}}, nil
	})
	m := newModel(sess, perm.Chat, nil)
	m.resize(60, 24)

	drainExchange(t, &m, "hello")

	report := m.usageReport()
	for _, want := range []string{
		"requests  1 of 20",
		"remaining 19",
		"by provider",
		"stub",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("usage report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "no requests yet this session") {
		t.Errorf("usage report claims no requests after one:\n%s", report)
	}
}

// An uncapped session must say so rather than printing a remaining count
// against a cap of zero, which would read as "no requests left".
func TestUsageReportOnAnUncappedSessionSaysUncapped(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})

	report := m.usageReport()
	if !strings.Contains(report, "remaining uncapped") {
		t.Errorf("uncapped report missing the uncapped line:\n%s", report)
	}
	if strings.Contains(report, "remaining 0") {
		t.Errorf("uncapped report printed a zero remaining count:\n%s", report)
	}
}

// The footer tells the user what the keys do, and what they do changes with
// the model's state. Each state must advertise its own keys: naming ctrl+c as
// "stop" while idle, or as "quit" mid-question, would be wrong about what the
// next keystroke actually does. The window is made wide enough to hold the
// longest hint, since a narrow one is cut and that is a separate rule.
func TestFooterHintsMatchTheCurrentState(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m.width = 120

	idle := m.footer()
	if !strings.Contains(idle, "quit") {
		t.Errorf("idle footer does not mention quitting:\n%s", idle)
	}

	m.busy = true
	busy := m.footer()
	if !strings.Contains(busy, "stop") {
		t.Errorf("busy footer does not mention stopping:\n%s", busy)
	}

	m.asking = true
	asking := m.footer()
	if !strings.Contains(asking, "allow") || !strings.Contains(asking, "deny") {
		t.Errorf("asking footer does not offer both answers:\n%s", asking)
	}
	if strings.Contains(asking, "stop") {
		t.Errorf("asking footer promises a stop key the question has taken:\n%s", asking)
	}
}

func TestBusyFooterShowsActivityPhase(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m.width = 120
	m.busy = true

	for _, phase := range []string{"connecting", "streaming", "running read"} {
		m.activity = phase
		got := m.footer()
		if !strings.Contains(got, phase) {
			t.Errorf("busy footer %q does not show phase %q", got, phase)
		}
	}
}

// A narrow window must not push the input off the screen, so the footer is cut
// to the width rather than wrapped onto a second line.
func TestFooterIsCutToTheWindowWidth(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m.width = 12

	got := m.footer()
	for _, line := range strings.Split(got, "\n") {
		if n := len([]rune(strings.TrimRight(line, " "))); n > 12 {
			t.Errorf("footer line is %d cells wide at width 12: %q", n, line)
		}
	}
	if strings.Count(got, "\n") > 0 {
		t.Errorf("footer wrapped onto %d lines at width 12:\n%s", strings.Count(got, "\n")+1, got)
	}
}

// drainExchange submits text and runs the resulting command chain to
// completion, the way Bubble Tea's loop would, so that a test can assert on
// what the session recorded once the reply has finished arriving.
func drainExchange(t *testing.T, m *model, text string) {
	t.Helper()

	*m = typing(*m, text)
	next, cmd := step(t, *m, tea.KeyMsg{Type: tea.KeyEnter})
	*m = next
	if cmd == nil {
		t.Fatal("submitting returned no command to drain")
	}

	for i := 0; cmd != nil && i < 64; i++ {
		msg := cmd()
		next, cmd = step(t, *m, msg)
		*m = next
		if _, closed := msg.(streamClosedMsg); closed {
			return
		}
	}
	t.Fatal("the exchange never reported the stream closed")
}
