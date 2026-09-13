// This file holds the reaction half of the model: key handling, resizing, and
// the arrival of streamed events. Update stays pure — it touches no terminal and
// starts no work of its own beyond returning commands — so that a test can feed
// it messages in sequence and read the resulting state directly.
package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Update satisfies tea.Model.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case streamEventMsg:
		return m.handleEvent(msg)

	case streamClosedMsg:
		// A close from a superseded stream says nothing about the
		// exchange currently in flight, so it must not clear busy.
		if msg.seq != m.seq {
			return m, nil
		}
		m.finish()
		return m, nil

	case approvalRequestMsg:
		// The reader is re-issued straight away rather than after the
		// answer: the channel is unbuffered, so the next tool to ask
		// stays parked in its send until this question is off screen,
		// and one pending read is all it takes to accept it then.
		m.ask(msg.request)
		return m, waitForApproval(m.approvals)

	case approvalAnswerMsg:
		m.resolve(msg.request, msg.allowed)
		return m, nil
	}

	return m.forward(msg)
}

// handleKey applies the keys the session owns and forwards the rest.
func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// While a question is on screen the keyboard belongs to the question. It
	// is checked before anything else so that ctrl+c, Enter and the textarea
	// cannot answer it by accident, and so that the answer is always one
	// keystroke rather than a keystroke aimed at a hidden input.
	if m.asking {
		return m, answerApproval(m.question, allowsApproval(msg))
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		// While a reply is streaming, ctrl+c means "stop this", which is
		// what a user reaching for it during a runaway answer wants. It
		// only quits when there is nothing to interrupt.
		if m.busy {
			m.cancel()
			return m, nil
		}
		return m, tea.Quit

	case tea.KeyEnter:
		return m.handleEnter()

	case tea.KeyCtrlJ:
		// Enter is spent on submitting, so a newline needs its own key.
		// The textarea inserts one in response to Enter, so that is what
		// it is handed.
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(tea.KeyMsg{Type: tea.KeyEnter})
		return m, cmd

	case tea.KeyPgUp, tea.KeyPgDown:
		// The textarea would swallow these to move its own cursor, but
		// with a three-line input there is nothing to page through
		// there and everything to page through in the transcript.
		var cmd tea.Cmd
		m.view, cmd = m.view.Update(msg)
		return m, cmd
	}

	return m.forward(msg)
}

// allowsApproval reports whether a keystroke is a yes.
//
// Only a bare y is, in either case. Every other key — n, esc, ctrl+c, Enter, a
// stray letter typed while the question appeared — is a no, because deny is the
// default and the way to reach a yes has to be deliberate. Alt is excluded so
// that a window-manager shortcut passing through cannot approve anything.
func allowsApproval(msg tea.KeyMsg) bool {
	if msg.Type != tea.KeyRunes || msg.Alt || len(msg.Runes) != 1 {
		return false
	}
	return msg.Runes[0] == 'y' || msg.Runes[0] == 'Y'
}

// handleEnter submits the input, if there is anything to submit.
func (m model) handleEnter() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}

	// A second prompt sent while the first is still streaming would
	// interleave two replies into one transcript, so the input is refused
	// rather than queued: the user keeps their text and can resend it.
	if m.busy {
		m.append(block{kind: blockNotice, text: "still answering — press ctrl+c to stop"})
		return m, nil
	}

	m.input.Reset()

	if strings.HasPrefix(text, "/") {
		return m.runCommand(text)
	}

	cmd := m.submit(text)
	return m, cmd
}

// handleEvent applies one packet from the stream in flight.
func (m model) handleEvent(msg streamEventMsg) (tea.Model, tea.Cmd) {
	// Packets from a cancelled or superseded exchange are dropped, but the
	// command is not re-issued for them: that stream's reader ends here.
	if msg.seq != m.seq || !m.busy {
		return m, nil
	}

	packet := msg.packet

	switch {
	case packet.err != nil:
		// An error does not end the stream — the session may still be
		// falling back to another provider — so busy stays set and the
		// close message remains the single point that clears it.
		m.append(block{kind: blockError, text: packet.err.Error()})

	case packet.event.Tool != nil:
		// The provider is recorded first: committing the text written
		// before the call tags it with whoever wrote it.
		if packet.event.Provider != "" {
			m.answered = packet.event.Provider
		}
		m.tool(packet.event.Tool)

	case packet.event.Notice != "":
		m.append(block{kind: blockNotice, text: packet.event.Notice})
		if packet.event.Provider != "" {
			m.answered = packet.event.Provider
		}

	default:
		if packet.event.Provider != "" {
			m.answered = packet.event.Provider
		}
		if packet.event.Delta != "" {
			m.pending += packet.event.Delta
			m.refresh()
		}
	}

	return m, waitForStream(m.current)
}

// forward hands a message to the focused child components.
func (m model) forward(msg tea.Msg) (tea.Model, tea.Cmd) {
	var inputCmd, viewCmd tea.Cmd
	m.input, inputCmd = m.input.Update(msg)
	m.view, viewCmd = m.view.Update(msg)
	return m, tea.Batch(inputCmd, viewCmd)
}
