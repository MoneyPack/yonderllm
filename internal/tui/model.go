// This file defines the model: the whole state of an interactive session, and
// the small helpers that keep the transcript and the layout consistent as that
// state changes. Update and View live in their own files so that this one stays
// a description of what a session *is* rather than how it reacts.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"yonderllm/internal/perm"
	"yonderllm/internal/session"
)

// Layout constants. The header is the brand line plus the rule beneath it; the
// footer is the keybinding hint. Both are fixed height so that the viewport can
// be sized by subtraction without measuring rendered strings.
const (
	headerHeight = 2
	footerHeight = 1
	inputHeight  = 3
	// minViewport keeps the transcript from collapsing to nothing on a
	// short terminal; the viewport scrolls, so a small window is usable
	// where a zero-height one is not.
	minViewport = 3
)

// model is the state of an interactive session.
type model struct {
	sess *session.Session
	mode perm.Mode

	styles styles
	input  textarea.Model
	view   viewport.Model

	// blocks is the transcript. It is the source of truth; the viewport
	// holds only a rendering of it, and is rebuilt whenever either the
	// blocks or the width change.
	blocks []block

	// current is the exchange in flight, valid only while busy. seq is
	// incremented for every exchange started, so that packets from a
	// cancelled stream can be recognised and dropped.
	current stream
	seq     int
	busy    bool
	// pending accumulates deltas for the reply being streamed. It is held
	// separately from the transcript so that a partial reply can be
	// discarded on cancellation without disturbing completed blocks.
	pending  string
	answered string

	width  int
	height int
	// ready is false until the first WindowSizeMsg arrives. Bubble Tea
	// sends one immediately on start, but rendering before it would use a
	// zero width and wrap every line to nothing.
	ready bool
}

// newModel builds a session model. It does not touch the terminal, so tests can
// drive Update and View directly.
func newModel(sess *session.Session, mode perm.Mode) model {
	s := newStyles()

	input := textarea.New()
	input.Placeholder = "Ask anything, or /help for commands"
	// Line numbers belong in an editor, not a prompt, and the character
	// limit exists to stop a runaway paste rather than to constrain
	// legitimate input.
	input.ShowLineNumbers = false
	input.CharLimit = 0
	input.SetHeight(inputHeight)
	input.Focus()

	m := model{
		sess:   sess,
		mode:   mode,
		styles: s,
		input:  input,
		view:   viewport.New(0, 0),
	}
	m.blocks = []block{{kind: blockInfo, text: m.greeting()}}
	return m
}

// greeting is the first thing in the transcript: it states which provider and
// model the session will actually use, because that is the fact a user is most
// likely to be wrong about when they start typing.
func (m model) greeting() string {
	return fmt.Sprintf(
		"Connected to %s (%s) in %s mode. Inference runs remotely; this machine only draws the session.\nType /help for commands.",
		m.sess.Provider(), m.sess.Model(), m.mode,
	)
}

// Init satisfies tea.Model.
func (m model) Init() tea.Cmd {
	return textarea.Blink
}

// resize recomputes the layout for a new terminal size.
func (m *model) resize(width, height int) {
	m.width = width
	m.height = height
	m.ready = true

	m.input.SetWidth(width)

	vh := height - headerHeight - footerHeight - inputHeight
	if vh < minViewport {
		vh = minViewport
	}
	m.view.Width = width
	m.view.Height = vh
	m.refresh()
}

// append adds a block to the transcript and scrolls to it.
func (m *model) append(b block) {
	m.blocks = append(m.blocks, b)
	m.refresh()
}

// refresh re-renders the transcript into the viewport.
//
// The whole transcript is rebuilt on every change rather than appended to,
// because a width change reflows every block, and one code path that is always
// exercised is worth more than a faster one that is only usually correct.
func (m *model) refresh() {
	width := m.width
	if width < 8 {
		width = 8
	}

	parts := make([]string, 0, len(m.blocks)+1)
	for _, b := range m.blocks {
		parts = append(parts, b.render(m.styles, width))
	}
	// The reply in flight is rendered as a block that does not yet exist in
	// the transcript, so that text appears as it streams without half a
	// reply being committed to the history the user scrolls back through.
	if m.busy && m.pending != "" {
		parts = append(parts, block{kind: blockAssistant, tag: m.answered, text: m.pending}.render(m.styles, width))
	}

	m.view.SetContent(strings.Join(parts, "\n\n"))
	m.view.GotoBottom()
}

// submit starts an exchange for prompt.
func (m *model) submit(prompt string) tea.Cmd {
	m.seq++
	m.busy = true
	m.pending = ""
	m.answered = m.sess.Provider()

	m.append(block{kind: blockUser, text: prompt})

	m.current = startStream(m.sess, m.seq, prompt)
	return waitForStream(m.current)
}

// finish ends the exchange in flight, committing whatever text arrived.
func (m *model) finish() {
	m.busy = false
	if text := strings.TrimSpace(m.pending); text != "" {
		m.blocks = append(m.blocks, block{kind: blockAssistant, tag: m.answered, text: text})
	}
	m.pending = ""
	m.refresh()
}

// cancel stops the exchange in flight. The partial reply is kept: the user saw
// it on screen, and silently deleting text they have already read is worse than
// keeping a reply that is visibly cut short.
func (m *model) cancel() {
	if !m.busy {
		return
	}
	m.current.cancel()
	m.finish()
	m.append(block{kind: blockNotice, text: "cancelled"})
}
