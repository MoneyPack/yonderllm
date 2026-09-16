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
	"yonderllm/internal/provider"
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

	m := model{
		sess:      sess,
		mode:      mode,
		styles:    s,
		input:     input,
		view:      viewport.New(0, 0),
		approvals: approvals,
		sessions:  sessions,
	}
	m.blocks = []block{{kind: blockInfo, text: m.greeting()}}
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
	return fmt.Sprintf(
		"Connected to %s (%s) in %s mode. Inference runs remotely; this machine only draws the session.\nType /help for commands.",
		m.sess.Provider(), m.sess.Model(), m.mode,
	)
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
	for i, existing := range m.blocks {
		if existing.kind == blockTool && existing.id != "" && existing.id == run.ID {
			m.blocks[i] = b
			m.refresh()
			return
		}
	}
	m.append(b)
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

// resolve closes the open question, rewriting its block with the decision.
//
// The block is matched on id rather than assumed to be last, because a question
// is answered from the message loop and nothing guarantees that no other block
// arrived in between.
func (m *model) resolve(req approvalRequest, allowed bool) {
	m.asking = false
	m.question = approvalRequest{}

	b := approvalBlock(req, approvalOutcome(allowed))
	for i, existing := range m.blocks {
		if existing.kind == blockApproval && existing.id != "" && existing.id == req.id {
			m.blocks[i] = b
			m.refresh()
			return
		}
	}
	m.append(b)
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
	m.commitPending()
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
