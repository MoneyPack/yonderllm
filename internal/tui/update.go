// This file holds the reaction half of the model: key handling, resizing, and
// the arrival of streamed events. Update stays pure — it touches no terminal and
// starts no work of its own beyond returning commands — so that a test can feed
// it messages in sequence and read the resulting state directly.
package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MoneyPack/yonderllm/internal/provider"
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

	case activityTickMsg:
		if msg.seq != m.seq || (!m.busy && !m.unwinding) {
			return m, nil
		}
		if m.busy {
			m.spinner++
		}
		return m, waitForStream(m.current)

	case streamClosedMsg:
		// A close from a superseded stream says nothing about the
		// exchange currently in flight, so it must not clear busy.
		if msg.seq != m.seq {
			return m, nil
		}
		m.closed()
		return m, nil

	case commandDoneMsg:
		m.commandDone(msg)
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
	//
	// A key that lands after the answer has gone but before its record has
	// come back was aimed at the question, not at the prompt, so it is
	// dropped rather than typed. This is also what keeps a quick "y" then
	// "n" from denying a call the tool was already told it could make.
	if m.decided != "" {
		return m, nil
	}
	if m.asking {
		if allowsApproval(msg) && (!m.ready || m.height < headerHeight+footerHeight+inputHeight+minViewport || m.width < 20) {
			return m, nil
		}
		return m, m.answer(allowsApproval(msg))
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

	case tea.KeyPgUp:
		// The textarea would swallow these to move its own cursor, but
		// with a three-line input there is nothing to page through
		// there and everything to page through in the transcript. The
		// viewport is told directly rather than handed the key, since
		// its own bindings were removed so that typing cannot scroll.
		m.view.PageUp()
		return m, nil

	case tea.KeyPgDown:
		m.view.PageDown()
		return m, nil

	case tea.KeyCtrlHome, tea.KeyCtrlEnd:
		// The textarea binds these to the start and end of the input,
		// which alt+< and alt+> still reach; three lines of prompt need
		// them far less than a long transcript does.
		if msg.Type == tea.KeyCtrlHome {
			m.view.GotoTop()
		} else {
			m.view.GotoBottom()
		}
		return m, nil
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
	// A slash command working off the loop will report into the transcript
	// when it finishes; anything sent before then would land around its
	// result in an order nobody chose, so the input waits its turn.
	if m.command != "" {
		m.append(block{kind: blockNotice, text: "still running " + m.command + " — send again once it has finished"})
		return m, nil
	}
	// Cancellation releases the keyboard before the worker has necessarily
	// released Session. Preserve input until all its reads/writes have ended.
	if m.stopping() {
		m.append(block{kind: blockNotice, text: "still stopping — send again once the exchange has stopped"})
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
	// Packets from a superseded exchange are dropped, and the command is
	// not re-issued for them: that stream's reader ends here.
	if msg.seq != m.seq {
		return m, nil
	}
	// Packets from the cancelled exchange are dropped too, but its reader
	// carries on: the close is what says the worker has stopped, and
	// nothing else would ever observe it.
	if !m.busy {
		if m.unwinding {
			return m, waitForStream(m.current)
		}
		return m, nil
	}

	packet := msg.packet

	switch {
	case packet.err != nil:
		// An error does not end the stream — the session may still be
		// falling back to another provider — so busy stays set and the
		// close message remains the single point that clears it.
		m.append(block{kind: blockError, text: packet.err.Error() + "\n" + provider.FailureHint(packet.err)})
		m.activity = "recovering"

	case packet.event.Tool != nil:
		// The provider is recorded first: committing the text written
		// before the call tags it with whoever wrote it.
		if packet.event.Provider != "" {
			m.answered = packet.event.Provider
		}
		m.tool(packet.event.Tool)
		if packet.event.Tool.Finished {
			m.activity = "streaming"
		} else {
			m.activity = "running " + packet.event.Tool.Name
		}

	case packet.event.Notice != "":
		m.commitPending()
		m.append(block{kind: blockNotice, text: packet.event.Notice})
		if packet.event.Provider != "" {
			m.answered = packet.event.Provider
		}

	default:
		m.activity = "streaming"
		if packet.event.Provider != "" {
			m.answered = packet.event.Provider
		}
		if packet.event.Delta != "" {
			// Only the reply in flight changed, so only it is
			// redrawn; the committed blocks come from the cache.
			m.pending += packet.event.Delta
			m.draw()
		}
	}

	return m, waitForStream(m.current)
}

type activityTickMsg struct{ seq int }

// forward hands a message to the child components.
//
// Keys go to the textarea alone. The viewport has bindings of its own for
// letters and arrows, and handing it the same keystroke would scroll the
// transcript with every character typed into the prompt; it is paged by the
// explicit keys in handleKey instead. Everything else — blink ticks, mouse
// events — reaches both.
func (m model) forward(msg tea.Msg) (tea.Model, tea.Cmd) {
	var inputCmd, viewCmd tea.Cmd
	m.input, inputCmd = m.input.Update(msg)
	if _, isKey := msg.(tea.KeyMsg); !isKey {
		m.view, viewCmd = m.view.Update(msg)
	}
	return m, tea.Batch(inputCmd, viewCmd)
}
