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

	"github.com/MoneyPack/yonderllm/internal/perm"
	"github.com/MoneyPack/yonderllm/internal/provider"
	"github.com/MoneyPack/yonderllm/internal/session"
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
	// holds only a rendering of it, redrawn whenever the blocks, the
	// width or the reply in flight change.
	blocks []block
	// rendered caches each committed block as drawn at renderedWidth, so
	// that a streamed delta re-wraps only the reply in flight and not the
	// whole transcript above it. It is dropped when the width changes or a
	// block is rewritten, and extended when blocks are appended.
	rendered      []string
	renderedWidth int
	// welcomed records that the last draw showed the welcome rather than
	// the transcript, so that the first real block can be scrolled into
	// view even though the welcome left the viewport wherever it was.
	welcomed bool

	// current is the exchange in flight, valid only while busy. seq is
	// incremented for every exchange started, so that packets from a
	// cancelled stream can be recognised and dropped.
	current  stream
	seq      int
	busy     bool
	activity string
	spinner  int
	// unwinding mirrors, for the footer, whether a cancelled exchange's
	// worker is still running. It is set by cancel and cleared when the
	// stream reports itself closed, so that View can read a field rather
	// than select on a channel. Update-side checks use stopping, which
	// asks the channel directly and so cannot lag behind.
	unwinding bool
	// pending accumulates deltas for the reply being streamed. It is held
	// separately from the transcript so that a partial reply can be
	// discarded on cancellation without disturbing completed blocks.
	pending  string
	answered string

	// approvals is the channel the tools ask their questions over, and
	// asking is whether one of those questions is on screen. While it is,
	// the keyboard belongs to the question: a goroutine inside a tool call
	// is parked waiting for the answer, and nothing else can come first.
	approvals *Approvals
	question  approvalRequest
	asking    bool
	// asked counts questions, only so that each one gets an id its answer
	// can be matched against when it comes to rewrite the block.
	asked int
	// decided is the id of a question whose answer has been sent back to
	// the tool but whose block has not yet been rewritten. The gap is one
	// command's round trip, but a second keystroke landing in it must not
	// answer the question again or be typed into the prompt by mistake.
	decided string

	// command names the slash command doing its work off the message loop,
	// and is empty when none is. commands counts them, so that each one's
	// placeholder gets an id its result can be matched against.
	command  string
	commands int

	// sessions is the optional on-disk store used by /save.
	sessions *session.Sessions

	width  int
	height int
	// ready is false until the first WindowSizeMsg arrives. Bubble Tea
	// sends one immediately on start, but rendering before it would use a
	// zero width and wrap every line to nothing.
	ready bool
}

// newModel builds a session model. It does not touch the terminal, so tests can
// drive Update and View directly.
//
// approvals may be nil, and is nil wherever the gated tools were built without
// an approver: with nothing able to ask, there is nothing to answer.
func newModel(sess *session.Session, mode perm.Mode, approvals *Approvals) model {
	return newModelWithSessions(sess, mode, approvals, nil)
}

// newModelWithSessions is newModel with an on-disk conversation store attached,
// for the TUI's /save. Passing nil keeps saving unavailable, which is how tests
// that do not exercise it stay unchanged.
func newModelWithSessions(sess *session.Session, mode perm.Mode, approvals *Approvals, sessions *session.Sessions) model {
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

	view := viewport.New(0, 0)
	// The viewport's own bindings — j/k, u/d, space, the arrows — would
	// scroll the transcript on every letter typed into the prompt. Keys
	// are routed to the textarea alone, and the bindings are removed as
	// well so that no other path into the viewport can claim a keystroke.
	// Paging is done by explicit calls from handleKey instead.
	view.KeyMap = viewport.KeyMap{}

	m := model{
		sess:      sess,
		mode:      mode,
		styles:    s,
		input:     input,
		view:      view,
		approvals: approvals,
		sessions:  sessions,
	}
	m.blocks = []block{{kind: blockInfo, tag: "welcome", text: m.greeting()}}
	for _, msg := range sess.History().Turns() {
		switch msg.Role {
		case provider.RoleUser:
			m.blocks = append(m.blocks, block{kind: blockUser, text: msg.Content})
		case provider.RoleAssistant:
			if msg.Content != "" {
				m.blocks = append(m.blocks, block{kind: blockAssistant, text: msg.Content, tag: sess.Provider()})
			}
			for _, call := range msg.ToolCalls {
				m.blocks = append(m.blocks, block{kind: blockInfo, text: "previous tool: " + call.Name + " " + call.Arguments})
			}
		case provider.RoleTool:
			m.blocks = append(m.blocks, block{kind: blockInfo, text: "previous tool result: " + msg.Content})
		}
	}
	return m
}

// greeting is the first thing in the transcript: it states which provider and
// model the session will actually use, because that is the fact a user is most
// likely to be wrong about when they start typing.
func (m model) greeting() string {
	text := fmt.Sprintf(
		"Using %s (%s) in %s mode. Inference runs remotely.\nType /help for commands.",
		m.sess.Provider(), m.sess.Model(), m.mode,
	)
	if m.sessions != nil {
		text += "\n/save <name> saves this conversation."
	}
	return text
}

// stopping reports whether a cancelled exchange's worker is still running. It
// asks the channel directly, so it is the check Update relies on before letting
// anything else touch the session; View reads the unwinding field instead.
func (m model) stopping() bool {
	if m.busy || m.current.done == nil {
		return false
	}
	select {
	case <-m.current.done:
		return false
	default:
		return true
	}
}

// Init satisfies tea.Model.
//
// Listening for approval requests starts here rather than when the first tool
// call happens, because the request arrives from a goroutine that is already
// parked: if nothing were reading the channel, a tool would hang before the
// question ever reached the screen.
func (m model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, waitForApproval(m.approvals))
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

// append adds a block to the transcript and draws it.
func (m *model) append(b block) {
	m.blocks = append(m.blocks, b)
	m.draw()
}

// replace rewrites the block of the given kind carrying id, and reports
// whether there was one. Callers append when there was not, so that a result
// whose announcement never reached the transcript is still recorded.
func (m *model) replace(k kind, id string, b block) bool {
	if id == "" {
		return false
	}
	for i, existing := range m.blocks {
		if existing.kind == k && existing.id == id {
			m.blocks[i] = b
			m.refresh()
			return true
		}
	}
	return false
}

// refresh re-renders the whole transcript into the viewport.
//
// It is the call for a change that draw cannot see: a block rewritten in place,
// the transcript replaced, or a resize. The cache of rendered blocks is dropped
// so that every block is wrapped again at the current width.
func (m *model) refresh() {
	m.rendered = m.rendered[:0]
	m.draw()
}

// draw pushes the transcript into the viewport, rendering only what the cache
// does not already hold: blocks appended since the last draw, and the reply in
// flight. That reply changes on every delta, and re-wrapping the whole
// transcript for each token made a long session slower with every answer.
//
// The reader's place is kept. The view follows new text only when it was already
// at the bottom, so scrolling back while a reply streams is not undone by the
// next token; a view that is following stays at the bottom as content grows.
func (m *model) draw() {
	width := max(m.width, 8)
	if width != m.renderedWidth || len(m.rendered) > len(m.blocks) {
		m.rendered = m.rendered[:0]
		m.renderedWidth = width
	}
	for _, b := range m.blocks[len(m.rendered):] {
		m.rendered = append(m.rendered, b.render(m.styles, width))
	}

	if m.showWelcome() {
		// The welcome is presentation rather than transcript: it is drawn
		// in the viewport's place and pinned to the top, and never joins
		// the blocks the user scrolls back through.
		m.view.SetContent(m.welcome())
		m.view.GotoTop()
		m.welcomed = true
		return
	}
	// A view that has only shown the welcome so far has no place to keep,
	// so the first real block is always scrolled into view.
	follow := m.welcomed || m.view.AtBottom()
	m.welcomed = false

	var sb strings.Builder
	for i, part := range m.rendered {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(part)
	}
	// The reply in flight is rendered as a block that does not yet exist in
	// the transcript, so that text appears as it streams without half a
	// reply being committed to the history the user scrolls back through.
	if m.busy && m.pending != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(block{kind: blockAssistant, tag: m.answered, text: m.pending}.render(m.styles, width))
	}

	m.view.SetContent(sb.String())
	if follow {
		m.view.GotoBottom()
	}
}

// showWelcome reports whether the viewport should show the welcome instead of
// the transcript: only while the transcript holds nothing but the greeting and
// nothing is happening that the welcome would hide.
func (m model) showWelcome() bool {
	return len(m.blocks) == 1 && m.blocks[0].tag == "welcome" && !m.busy && !m.asking
}

// commitPending moves the reply in flight into the transcript.
//
// It is called both when an exchange ends and when a tool call interrupts one,
// because in either case the text already on screen has stopped growing and
// belongs above whatever comes next. Refreshing is left to the caller, which is
// about to change the transcript again anyway.
func (m *model) commitPending() {
	if text := strings.TrimSpace(m.pending); text != "" {
		m.blocks = append(m.blocks, block{kind: blockAssistant, tag: m.answered, text: text})
	}
	m.pending = ""
}

// tool records a tool call, or rewrites the record of one already shown.
//
// A call reaches the transcript twice: once when the model sends it, so that a
// slow search is visibly in progress rather than a hang, and once with what it
// returned. The second arrival replaces the first, matched on the call's id, so
// the transcript ends up with one entry per call.
func (m *model) tool(run *session.ToolRun) {
	// Anything the model said before reaching for a tool belongs above the
	// call. Leaving it in pending would put it below, because refresh draws
	// the reply in flight after every committed block.
	m.commitPending()

	b := toolBlock(run)
	if !m.replace(blockTool, run.ID, b) {
		m.append(b)
	}
}

// toolBlock builds the transcript entry for one tool call.
func toolBlock(run *session.ToolRun) block {
	result := run.Result
	if run.Err != "" {
		// A tool that fails is not a failed exchange: the error goes back
		// to the model, which usually tries something else. So it stays a
		// tool block, keeping its place in the sequence and its id for the
		// match above, rather than being promoted to an error block and
		// shouted about.
		result = "error: " + run.Err
	}
	return block{
		kind: blockTool,
		tag:  run.Name,
		id:   run.ID,
		text: toolView(run.Arguments, result),
	}
}

// ask puts a question on screen and gives the keyboard to it.
//
// The question is a transcript block like any other, so it keeps its place in the
// sequence: the user sees what the model said, then the call it wants to make,
// then their own decision, in the order those things happened.
func (m *model) ask(req approvalRequest) {
	// Whatever the model said on its way to this call belongs above the
	// question, for the same reason it does above a tool call.
	m.commitPending()

	m.asked++
	req.id = fmt.Sprintf("approval-%d", m.asked)

	m.question = req
	m.asking = true
	m.append(approvalBlock(req, approvalPrompt))
}

// answer takes the open question off the keyboard and sends the decision back
// to the tool. The block keeps the prompt until the answer message comes back
// through the loop and resolve rewrites it; in between, decided marks the
// question as spoken for.
//
// Clearing asking here rather than when the message arrives is what stops a
// second keystroke from answering again: with the question already off the
// keyboard, the second key has nothing to answer.
func (m *model) answer(allowed bool) tea.Cmd {
	req := m.question
	m.asking = false
	m.question = approvalRequest{}
	m.decided = req.id
	return answerApproval(req, allowed)
}

// resolve records the answer to a question, rewriting its block with the
// decision.
//
// Only the question whose answer is in flight, or the one still open, is
// accepted. Anything else is a second answer to a question already settled,
// and letting it through would let a stray keystroke rewrite an allowed call as
// denied after the tool had already been told yes.
//
// The block is matched on id rather than assumed to be last, because a question
// is answered from the message loop and nothing guarantees that no other block
// arrived in between.
func (m *model) resolve(req approvalRequest, allowed bool) {
	switch {
	case req.id != "" && req.id == m.decided:
		m.decided = ""
	case m.asking && req.id == m.question.id:
		m.asking = false
		m.question = approvalRequest{}
	default:
		return
	}
	m.record(req, approvalOutcome(allowed))
}

// abandon closes a question that the exchange ended before anyone answered it.
//
// The tool has already taken the end of its context as a no, so the reply is a
// formality; what matters is that the transcript says what happened and that
// the keyboard is handed back, because a prompt asking a question nobody can
// answer would swallow every key, ctrl+c included, for the rest of the session.
func (m *model) abandon() {
	if !m.asking {
		return
	}
	req := m.question
	m.asking = false
	m.question = approvalRequest{}

	select {
	case req.reply <- false:
	default:
	}
	m.record(req, approvalEnded)
}

// record writes a question's outcome into its block.
func (m *model) record(req approvalRequest, outcome string) {
	b := approvalBlock(req, outcome)
	if !m.replace(blockApproval, req.id, b) {
		m.append(b)
	}
}

// approvalBlock builds the transcript entry for one question.
func approvalBlock(req approvalRequest, outcome string) block {
	return block{
		kind: blockApproval,
		tag:  req.action.String(),
		id:   req.id,
		text: approvalView(req, outcome),
	}
}

// submit starts an exchange for prompt.
//
// A new exchange can only start once the previous worker has stopped, so any
// unwinding still recorded is over; clearing it here matters because the old
// stream's close, if it has not landed yet, will arrive under a stale sequence
// number and be dropped without clearing it.
func (m *model) submit(prompt string) tea.Cmd {
	m.seq++
	m.unwinding = false
	m.busy = true
	m.activity = "connecting"
	m.spinner = 0
	m.pending = ""
	m.answered = m.sess.Provider()

	m.append(block{kind: blockUser, text: prompt})

	m.current = startStream(m.sess, m.seq, prompt)
	return waitForStream(m.current)
}

// finish ends the exchange in flight, committing whatever text arrived and
// closing any question it left open. A question outlives its exchange only
// when the exchange ended from underneath it — a timeout, a provider giving up
// — and then there is nobody left to act on an answer.
func (m *model) finish() {
	m.busy = false
	m.activity = ""
	m.abandon()
	m.commitPending()
	m.refresh()
}

// cancel stops the exchange in flight. The partial reply is kept: the user saw
// it on screen, and silently deleting text they have already read is worse than
// keeping a reply that is visibly cut short.
//
// The notice carries an id so that, when the stream closes and the interrupted
// answer turns out to be retryable, the hint can be written onto this line
// rather than added as a second one under it.
func (m *model) cancel() {
	if !m.busy {
		return
	}
	m.current.cancel()
	m.finish()
	m.unwinding = true
	m.append(block{kind: blockNotice, id: m.cancelID(), text: "cancelled"})
}

// cancelID names the cancellation notice for the exchange in flight.
func (m model) cancelID() string {
	return fmt.Sprintf("cancel-%d", m.seq)
}

// closed handles the stream in flight reporting its last packet: the exchange
// is over, the worker has stopped, and an interrupted answer is offered for
// retry. After a cancellation the offer is folded into the "cancelled" line so
// the transcript reads as one event rather than two.
func (m *model) closed() {
	m.finish()
	m.unwinding = false

	if m.sess.CanRetry() != nil {
		return
	}
	hint := block{kind: blockNotice, id: m.cancelID(), text: "cancelled — " + retryHint}
	if !m.replace(blockNotice, m.cancelID(), hint) {
		m.append(block{kind: blockNotice, text: "answer interrupted — " + retryHint})
	}
}

// retryHint is what the transcript offers after an answer that stopped short.
const retryHint = "/retry requests an answer with tools disabled"
