package perm

import (
	"strings"
	"testing"
)

// TestPolicyMatrix pins the full mode/action table from SPEC.md section 6.
func TestPolicyMatrix(t *testing.T) {
	cases := []struct {
		mode   Mode
		action Action
		want   Decision
	}{
		// Chat touches nothing.
		{Chat, Read, Deny},
		{Chat, Search, Deny},
		{Chat, Write, Deny},
		{Chat, Exec, Deny},

		// Code reads freely, proposes patches, never execs.
		{Code, Read, Allow},
		{Code, Search, Allow},
		{Code, Write, Ask},
		{Code, Exec, Deny},

		// Agent does everything, asking before it changes anything.
		{Agent, Read, Allow},
		{Agent, Search, Allow},
		{Agent, Write, Ask},
		{Agent, Exec, Ask},
	}

	for _, c := range cases {
		if got := New(c.mode).Check(c.action); got != c.want {
			t.Errorf("mode=%s action=%s: got %s, want %s", c.mode, c.action, got, c.want)
		}
	}
}

// TestAutoApproveOnlyRelaxesAgent guards the rule that auto-approval is an
// Agent-mode convenience and must never loosen Chat or Code.
func TestAutoApproveOnlyRelaxesAgent(t *testing.T) {
	if got := NewAutoApprove(Agent).Check(Write); got != Allow {
		t.Errorf("agent auto-approve write: got %s, want %s", got, Allow)
	}
	if got := NewAutoApprove(Agent).Check(Exec); got != Allow {
		t.Errorf("agent auto-approve exec: got %s, want %s", got, Allow)
	}

	// Chat stays sealed even if auto-approval is somehow set.
	for _, a := range []Action{Read, Search, Write, Exec} {
		if got := NewAutoApprove(Chat).Check(a); got != Deny {
			t.Errorf("chat auto-approve %s: got %s, want %s", a, got, Deny)
		}
	}

	// Code must not gain exec, and its writes still require approval.
	if got := NewAutoApprove(Code).Check(Exec); got != Deny {
		t.Errorf("code auto-approve exec: got %s, want %s", got, Deny)
	}
	if got := NewAutoApprove(Code).Check(Write); got != Ask {
		t.Errorf("code auto-approve write: got %s, want %s", got, Ask)
	}
}

// TestDestructiveAlwaysConfirmed covers the spec rule that a destructive
// command is confirmed regardless of the configured approval level.
func TestDestructiveAlwaysConfirmed(t *testing.T) {
	if !NewAutoApprove(Agent).Confirm(Exec, true) {
		t.Error("destructive exec under auto-approve: got no confirmation, want confirmation")
	}
	if NewAutoApprove(Agent).Confirm(Exec, false) {
		t.Error("ordinary exec under auto-approve: got confirmation, want none")
	}
	// A denied action is refused outright, not confirmed.
	if New(Chat).Confirm(Exec, true) {
		t.Error("destructive exec in chat: got confirmation, want outright denial")
	}
}

// TestDefaultModeIsChat pins the zero value, since a Policy or Mode created
// without an explicit mode must be the safe one.
func TestDefaultModeIsChat(t *testing.T) {
	var m Mode
	if m != Chat {
		t.Errorf("zero Mode: got %s, want %s", m, Chat)
	}
	var p Policy
	if got := p.Check(Read); got != Deny {
		t.Errorf("zero Policy read: got %s, want %s", got, Deny)
	}
}

func TestParseModeRoundTrip(t *testing.T) {
	for _, m := range []Mode{Chat, Code, Agent} {
		got, err := ParseMode(m.String())
		if err != nil {
			t.Errorf("ParseMode(%q): unexpected error: %v", m.String(), err)
			continue
		}
		if got != m {
			t.Errorf("ParseMode(%q): got %s, want %s", m.String(), got, m)
		}
	}
	if _, err := ParseMode("root"); err == nil {
		t.Error("ParseMode(\"root\"): got nil error, want failure")
	}
}

// TestParseModeRejectsNearMisses guards the names that are close enough to a
// real mode that a silent acceptance would be an escalation bug. It also pins
// the error text, since that string is what the user sees on a typo.
func TestParseModeRejectsNearMisses(t *testing.T) {
	for _, s := range []string{"", "Chat", "CODE", " agent", "agent ", "chatty"} {
		got, err := ParseMode(s)
		if err == nil {
			t.Errorf("ParseMode(%q): got nil error, want failure", s)
			continue
		}
		// A rejected parse must still hand back the safe mode, because
		// callers that ignore the error must not land in Agent.
		if got != Chat {
			t.Errorf("ParseMode(%q): got mode %s on error, want %s", s, got, Chat)
		}
		if !strings.Contains(err.Error(), "unknown permission mode") {
			t.Errorf("ParseMode(%q): error %q does not name the problem", s, err)
		}
		if !strings.Contains(err.Error(), "want chat, code, or agent") {
			t.Errorf("ParseMode(%q): error %q does not list the valid modes", s, err)
		}
	}
}

// TestModeStringNames pins the spelling of each mode. These strings are the
// config file values and the ParseMode inputs, so a rename is a breaking change.
func TestModeStringNames(t *testing.T) {
	cases := []struct {
		mode Mode
		want string
	}{
		{Chat, "chat"},
		{Code, "code"},
		{Agent, "agent"},
	}
	for _, c := range cases {
		if got := c.mode.String(); got != c.want {
			t.Errorf("Mode(%d).String(): got %q, want %q", int(c.mode), got, c.want)
		}
	}
	// An out-of-range mode must be obviously wrong rather than silently
	// reading as a real one.
	if got := Mode(9).String(); got != "Mode(9)" {
		t.Errorf("Mode(9).String(): got %q, want %q", got, "Mode(9)")
	}
}

// TestActionStringNames pins the action names that appear in denial messages.
func TestActionStringNames(t *testing.T) {
	cases := []struct {
		action Action
		want   string
	}{
		{Read, "read"},
		{Search, "search"},
		{Write, "write"},
		{Exec, "exec"},
	}
	for _, c := range cases {
		if got := c.action.String(); got != c.want {
			t.Errorf("Action(%d).String(): got %q, want %q", int(c.action), got, c.want)
		}
	}
	if got := Action(7).String(); got != "Action(7)" {
		t.Errorf("Action(7).String(): got %q, want %q", got, "Action(7)")
	}
}

// TestDecisionStringNames pins the decision names used in tests and logs.
func TestDecisionStringNames(t *testing.T) {
	cases := []struct {
		decision Decision
		want     string
	}{
		{Deny, "deny"},
		{Allow, "allow"},
		{Ask, "ask"},
	}
	for _, c := range cases {
		if got := c.decision.String(); got != c.want {
			t.Errorf("Decision(%d).String(): got %q, want %q", int(c.decision), got, c.want)
		}
	}
	if got := Decision(5).String(); got != "Decision(5)" {
		t.Errorf("Decision(5).String(): got %q, want %q", got, "Decision(5)")
	}
}

// TestPolicyModeReportsItsMode covers the accessor the TUI uses to render the
// mode indicator, including the auto-approve constructor.
func TestPolicyModeReportsItsMode(t *testing.T) {
	for _, m := range []Mode{Chat, Code, Agent} {
		if got := New(m).Mode(); got != m {
			t.Errorf("New(%s).Mode(): got %s, want %s", m, got, m)
		}
		if got := NewAutoApprove(m).Mode(); got != m {
			t.Errorf("NewAutoApprove(%s).Mode(): got %s, want %s", m, got, m)
		}
	}
	var p Policy
	if got := p.Mode(); got != Chat {
		t.Errorf("zero Policy Mode(): got %s, want %s", got, Chat)
	}
}

// TestConfirmNeverPromptsForDeniedActions checks that Confirm refuses rather
// than prompts, so a denied action can never be talked into running.
func TestConfirmNeverPromptsForDeniedActions(t *testing.T) {
	// Every chat action is denied, destructive or not.
	for _, a := range []Action{Read, Search, Write, Exec} {
		for _, destructive := range []bool{true, false} {
			if New(Chat).Confirm(a, destructive) {
				t.Errorf("chat confirm %s (destructive=%v): got confirmation, want outright denial", a, destructive)
			}
		}
	}
	// Code cannot exec, so even a destructive exec is refused outright.
	if New(Code).Confirm(Exec, true) {
		t.Error("code confirm destructive exec: got confirmation, want outright denial")
	}
	// A permitted-but-destructive write is confirmed.
	if !New(Agent).Confirm(Write, true) {
		t.Error("agent confirm destructive write: got no confirmation, want confirmation")
	}
}

// TestDeniedErrorMessage pins the refusal text, which is user-facing.
func TestDeniedErrorMessage(t *testing.T) {
	err := &DeniedError{Mode: Chat, Action: Exec}
	got := err.Error()
	if got != "exec is not permitted in chat mode" {
		t.Errorf("DeniedError.Error(): got %q, want %q", got, "exec is not permitted in chat mode")
	}

	// The zero value must still read as a sentence rather than as integers.
	var zero DeniedError
	if zero.Error() != "read is not permitted in chat mode" {
		t.Errorf("zero DeniedError.Error(): got %q, want %q", zero.Error(), "read is not permitted in chat mode")
	}
}
