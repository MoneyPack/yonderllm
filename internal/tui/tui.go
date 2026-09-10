// Package tui is the interactive face of yonderllm: a full-screen terminal
// session in which prompts are typed, replies stream in, and slash commands
// adjust the provider, model and transcript without leaving the keyboard.
//
// Inference happens somewhere else. This package draws, buffers keystrokes and
// shuttles bytes; every token it renders arrived over the network from a
// session.Session. The model, its update loop and its view are deliberately
// pure — they take messages and return state — so that the whole interface can
// be driven in a test without a terminal attached. This file is the only place
// that touches the outside world.
package tui

import (
	"fmt"
	"io"

	tea "github.com/charmbracelet/bubbletea"

	"yonderllm/internal/perm"
	"yonderllm/internal/session"
)

// Options configures an interactive session.
//
// Session is required; everything the interface can do — asking, switching
// providers, reporting usage — is a method on it. Mode is the permission mode
// shown in the header. In and Out are optional: when nil the program reads the
// real terminal and writes to it, which is what the command line wants, and
// when supplied the program is driven by those streams instead, which is what a
// test wants.
type Options struct {
	Session *session.Session
	Mode    perm.Mode
	In      io.Reader
	Out     io.Writer
}

// Run opens an interactive session and blocks until the user quits.
//
// The alternate screen is used so that the transcript does not scroll away the
// shell history behind it; on exit the terminal is restored to what it was.
func Run(opts Options) error {
	if opts.Session == nil {
		return fmt.Errorf("tui: no session")
	}

	teaOpts := []tea.ProgramOption{tea.WithAltScreen()}
	if opts.In != nil {
		teaOpts = append(teaOpts, tea.WithInput(opts.In))
	}
	if opts.Out != nil {
		teaOpts = append(teaOpts, tea.WithOutput(opts.Out))
	}

	if _, err := tea.NewProgram(newModel(opts.Session, opts.Mode), teaOpts...).Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}
