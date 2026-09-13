package cli

import (
	"io"
	"os"

	"yonderllm/internal/perm"
	"yonderllm/internal/tui"
)

// This file is the seam between the command line and the full-screen
// interface. The cli package owns the decision of *when* to open the TUI —
// which depends on flags, arguments and whether a human is actually at the
// keyboard — while the tui package owns everything that happens afterwards.

// runTUI resolves configuration and hands a live session to the interactive
// interface, blocking until the user quits.
//
// Configuration is resolved here rather than through newSession because the
// interface needs the permission mode as well as the session, and the mode
// lives on the [config.Config] that newSession discards. Resolving once and
// using both halves keeps the two in step: the mode shown in the header is
// always the mode the session was built with.
func (e *env) runTUI() error {
	cfg, err := e.resolve()
	if err != nil {
		return err
	}

	// resolve has already rejected an unparseable mode, so this cannot
	// fail; the error is still checked rather than discarded because a
	// future change to resolve should not silently start defaulting to chat.
	mode, err := perm.ParseMode(cfg.Mode)
	if err != nil {
		return err
	}

	// The approvals bridge is created before the session because the session
	// needs its Ask method and the interface needs the value itself. It is
	// one object seen from two sides: a tool call sends a question down it,
	// the message loop reads that question out. Creating it here — the only
	// place that knows a human is at the keyboard — is what turns perm.Ask
	// from a withheld capability into a real prompt.
	approvals := tui.NewApprovals()

	return tui.Run(tui.Options{
		Session:   e.newSessionFor(cfg, mode, approvals.Ask),
		Mode:      mode,
		Approvals: approvals,
		In:        e.in,
		Out:       e.out,
	})
}

// interactiveStdin reports whether r is a terminal a person is typing at.
//
// The full-screen interface is only meaningful with a human at the other end:
// opening it for `echo hi | yonderllm` would swallow the pipe and hang, and
// opening it under a test harness would hang the test. A character device on
// standard input is the portable signal that neither is happening, and it is
// available from the standard library alone — no terminal dependency is worth
// adding for one bit of information.
//
// A reader that is not an *os.File — a pipe buffer, a strings.Reader in a test
// — is by definition not a terminal, so the answer is no.
func interactiveStdin(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok || f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
