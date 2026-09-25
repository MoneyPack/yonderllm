// This file owns the other bridge into the message loop: the one that carries a
// question the *tools* need answered back to the person sitting in front of the
// terminal.
//
// A tool call runs on the goroutine draining the session, deep inside a provider
// exchange, and it needs a yes or no before it can continue. Update must never
// block, so the question travels as a message and the answer travels back down a
// channel the asking goroutine is parked on. Deny is the default everywhere in
// here: a cancelled exchange, a shut-down interface or a keystroke that is not
// plainly "yes" all come back as no.
package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MoneyPack/yonderllm/internal/perm"
	"github.com/MoneyPack/yonderllm/internal/terminaltext"
	"github.com/MoneyPack/yonderllm/internal/tools"
)

// maxApprovalLines caps how much of a request's detail reaches the prompt. The
// detail is a diff or an argument vector, and it exists so the user can judge
// the call; a diff longer than a screen has stopped helping them judge anything.
const maxApprovalLines = 40

// approvalRequest is one question waiting on an answer.
//
// It carries the reply channel with it rather than being answered through a
// shared one, so that a late answer to an abandoned question cannot be delivered
// to the next question by mistake.
type approvalRequest struct {
	action perm.Action
	target string
	detail string
	// id names the transcript block the question is drawn in, so that the
	// answer can replace the question rather than being appended under it.
	id string
	// reply has room for one answer, which is what lets the interface answer
	// and move on without waiting for the asking goroutine to be scheduled.
	reply chan bool
}

// Approvals is the channel the interface answers tool requests over.
//
// It exists as a separate value because of an ordering problem: the session and
// its tools are built before there is an interface to ask, so the approver
// handed to the tools has to be an indirection that the interface picks up
// afterwards. The command line creates one of these, gives Ask to the tools and
// the value itself to Run.
type Approvals struct {
	// ch is unbuffered on purpose. A request must not be accepted until the
	// interface is actually ready to read it, because accepting one would
	// let a second question queue up behind a prompt that is still on
	// screen.
	ch chan approvalRequest
}

// NewApprovals builds an approval channel.
func NewApprovals() *Approvals {
	return &Approvals{ch: make(chan approvalRequest)}
}

// Ask puts a request to the user and blocks until it is answered. It has the
// shape of a tools.Approver, which is how the tools reach the terminal.
//
// Both waits are guarded by the caller's context: when the exchange is cancelled
// — the user pressed ctrl+c, the provider gave up — the answer is no, because a
// question nobody can answer any more must not become a yes.
func (a *Approvals) Ask(ctx context.Context, req tools.Request) bool {
	request := approvalRequest{
		action: req.Action,
		target: req.Target,
		detail: req.Detail,
		reply:  make(chan bool, 1),
	}

	select {
	case a.ch <- request:
	case <-ctx.Done():
		return false
	}

	select {
	case answer := <-request.reply:
		return answer
	case <-ctx.Done():
		return false
	}
}

// approvalRequestMsg delivers a question to Update.
type approvalRequestMsg struct {
	request approvalRequest
}

// approvalAnswerMsg records that a question has been answered, so that Update
// can rewrite the block and carry on. The answer itself has already gone back to
// the asking goroutine by the time this arrives; this message is about the
// transcript, not about the tool.
type approvalAnswerMsg struct {
	request approvalRequest
	allowed bool
}

// answer sends a decision back to the goroutine waiting inside Ask.
//
// The send cannot block: the reply channel has room for one answer and only ever
// receives one. That matters because this runs on the message loop, and a loop
// parked in a send is an interface that has stopped redrawing.
func answerApproval(req approvalRequest, allowed bool) tea.Cmd {
	return func() tea.Msg {
		select {
		case req.reply <- allowed:
		default:
			// Nobody is waiting any more — the exchange was
			// cancelled while the question was on screen. Ask has
			// already returned no, which is the same answer this
			// path would have been allowed to give.
		}
		return approvalAnswerMsg{request: req, allowed: allowed}
	}
}

// waitForApproval reads a single question. Like the stream reader it is re-issued
// once its message has been handled, so that the interface only ever holds one
// question at a time and the loop is never parked inside a receive.
func waitForApproval(a *Approvals) tea.Cmd {
	if a == nil {
		return nil
	}
	return func() tea.Msg {
		request, ok := <-a.ch
		if !ok {
			// The channel is never closed in practice; if it ever is,
			// there is nothing left to ask about and no reason to
			// re-issue this command.
			return nil
		}
		return approvalRequestMsg{request: request}
	}
}

// approvalView renders a question, or the record of one already answered.
//
// The target leads, because it is what the user is deciding about; the detail —
// the diff for a write, the exact argument vector for a command — follows, and
// the outcome line is last so that the transcript reads as a question followed
// by its answer in the place the question was asked.
func approvalView(req approvalRequest, outcome string) string {
	var b strings.Builder
	b.WriteString(terminaltext.Line(req.target))

	if detail := strings.TrimRight(req.detail, "\n"); detail != "" {
		b.WriteString("\n")
		b.WriteString(clipLines(detail, maxApprovalLines))
	}

	b.WriteString("\n\n")
	b.WriteString(outcome)
	return b.String()
}

// approvalPrompt is the outcome line while the question is open. It names the
// default, because the one thing a user must not have to guess is what happens
// if they get it wrong.
const approvalPrompt = "allow? y allow · n deny · esc deny (deny is the default)"

const (
	// approvalAllowed and approvalDenied replace the prompt once the
	// question is answered. They are written in the past tense so that
	// scrolling back through a session reads as a history of decisions
	// rather than as a screen full of questions still waiting.
	approvalAllowed = "allowed"
	approvalDenied  = "denied"
	// approvalEnded is the outcome when the exchange finished before the
	// question was answered. It is still a denial — the tool took the end
	// of its context as a no — but one nobody chose, and the transcript
	// should not read as though they did.
	approvalEnded = "denied (exchange ended)"
)

// approvalOutcome names a decision.
func approvalOutcome(allowed bool) string {
	if allowed {
		return approvalAllowed
	}
	return approvalDenied
}
