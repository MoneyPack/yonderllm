// Package perm defines what a session is allowed to do.
//
// A session starts in Chat, the most restrictive mode. Escalation is always an
// explicit user action. Policy decisions live here rather than at each call
// site so that the rules can be tested in one place.
package perm

import "fmt"

// Mode is a permission level.
type Mode int

const (
	// Chat has no filesystem and no shell access. This is the default.
	Chat Mode = iota
	// Code may read and search the workspace and propose patches. Every
	// write requires approval.
	Code
	// Agent may read, search, write, and execute commands, subject to the
	// configured approval level.
	Agent
)

// String returns the name used in config, flags, and the UI.
func (m Mode) String() string {
	switch m {
	case Chat:
		return "chat"
	case Code:
		return "code"
	case Agent:
		return "agent"
	default:
		return fmt.Sprintf("Mode(%d)", int(m))
	}
}

// ParseMode resolves a mode name. It is the inverse of Mode.String.
func ParseMode(s string) (Mode, error) {
	switch s {
	case "chat":
		return Chat, nil
	case "code":
		return Code, nil
	case "agent":
		return Agent, nil
	default:
		return Chat, fmt.Errorf("unknown permission mode %q (want chat, code, or agent)", s)
	}
}

// Action is an operation a session may attempt.
type Action int

const (
	// Read opens a file inside the workspace.
	Read Action = iota
	// Search scans the workspace without opening a specific file.
	Search
	// Write creates, modifies, or deletes a file.
	Write
	// Exec runs a shell command.
	Exec
)

// String returns the action name used in denial messages.
func (a Action) String() string {
	switch a {
	case Read:
		return "read"
	case Search:
		return "search"
	case Write:
		return "write"
	case Exec:
		return "exec"
	default:
		return fmt.Sprintf("Action(%d)", int(a))
	}
}

// Decision is the outcome of a policy check.
type Decision int

const (
	// Deny means the action is not permitted in the current mode.
	Deny Decision = iota
	// Allow means the action may proceed without asking.
	Allow
	// Ask means the action may proceed only after the user approves it.
	Ask
)

// String returns the decision name, used in tests and logs.
func (d Decision) String() string {
	switch d {
	case Deny:
		return "deny"
	case Allow:
		return "allow"
	case Ask:
		return "ask"
	default:
		return fmt.Sprintf("Decision(%d)", int(d))
	}
}

// Policy resolves actions against a mode.
type Policy struct {
	mode Mode
	// autoApprove relaxes Agent-mode writes and execs from Ask to Allow. It
	// has no effect in Chat or Code: those modes' limits are not negotiable.
	autoApprove bool
}

// New returns a Policy for the given mode.
func New(mode Mode) Policy {
	return Policy{mode: mode}
}

// NewAutoApprove returns an Agent-mode Policy that does not prompt for writes
// and execs. Destructive commands are still confirmed; see Confirm.
func NewAutoApprove(mode Mode) Policy {
	return Policy{mode: mode, autoApprove: true}
}

// Mode reports the policy's mode, for display.
func (p Policy) Mode() Mode { return p.mode }

// Check resolves whether an action is permitted.
func (p Policy) Check(a Action) Decision {
	switch p.mode {
	case Chat:
		// Chat touches nothing outside the conversation.
		return Deny

	case Code:
		switch a {
		case Read, Search:
			return Allow
		case Write:
			// Code proposes patches; applying one needs approval.
			return Ask
		case Exec:
			return Deny
		}

	case Agent:
		switch a {
		case Read, Search:
			return Allow
		case Write, Exec:
			if p.autoApprove {
				return Allow
			}
			return Ask
		}
	}
	return Deny
}

// Confirm reports whether an action needs an explicit confirmation that
// auto-approval cannot waive. Destructive commands always do.
func (p Policy) Confirm(a Action, destructive bool) bool {
	if p.Check(a) == Deny {
		return false
	}
	return destructive
}

// DeniedError describes a refused action.
type DeniedError struct {
	Mode   Mode
	Action Action
}

func (e *DeniedError) Error() string {
	return fmt.Sprintf("%s is not permitted in %s mode", e.Action, e.Mode)
}
