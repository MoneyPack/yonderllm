// Package session owns a conversation: the running history, the trimming that
// keeps it inside a model's context window, token accounting against the daily
// cap, and the fallback across configured providers when one runs out of quota.
//
// Inference is remote, so the expensive resource this package protects is not
// CPU but the free-tier request and token budget.
package session

import (
	"slices"
	"unicode/utf8"

	"github.com/MoneyPack/yonderllm/internal/provider"
	"github.com/MoneyPack/yonderllm/internal/redact"
)

// tokensPerMessage approximates the per-message framing every chat API adds
// around content (role marker, separators). It keeps the estimate on the
// pessimistic side so trimming errs towards sending too little rather than
// overflowing the window and having the provider reject the request.
const tokensPerMessage = 4

// EstimateTokens approximates the token count of s.
//
// The real count is tokenizer-specific and only the provider knows it. Pulling
// in a tokenizer per provider would cost more than it is worth here: the
// estimate exists to decide what to drop, and a four-characters-per-token rule
// is close enough for English prose and conservative for code, which tokenizes
// more densely.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (utf8.RuneCountInString(s) + 3) / 4
}

// messageTokens estimates one message including its framing overhead.
//
// Tool calls are counted as well as content: an assistant turn that asks to
// run a tool often carries no text at all, and ignoring the arguments would
// let a long chain of tool calls slip past the budget uncounted.
func messageTokens(m provider.Message) int {
	total := EstimateTokens(m.Content) + tokensPerMessage
	for _, c := range m.ToolCalls {
		total += EstimateTokens(c.Name) + EstimateTokens(c.Arguments) + tokensPerMessage
	}
	return total
}

// TotalTokens estimates the cost of a whole message slice.
func TotalTokens(msgs []provider.Message) int {
	total := 0
	for _, m := range msgs {
		total += messageTokens(m)
	}
	return total
}

// History is an ordered conversation. The system prompt is held apart from the
// turns so that trimming can never discard it: dropping the instructions that
// shape every reply would change the assistant's behaviour mid-conversation,
// which is worse than dropping old context.
type History struct {
	system string
	turns  []provider.Message
}

// SetSystem replaces the system prompt. An empty string removes it.
func (h *History) SetSystem(prompt string) { h.system = prompt }

// System returns the current system prompt.
func (h *History) System() string { return h.system }

// Append adds a plain text turn to the end of the conversation.
func (h *History) Append(role provider.Role, content string) {
	h.turns = append(h.turns, provider.Message{Role: role, Content: content})
}

// AppendToolCalls records the assistant turn that asked to run tools.
//
// Content is kept even though it is usually empty: some models narrate what
// they are about to do in the same turn as the call, and dropping that text
// would leave a gap in the transcript.
func (h *History) AppendToolCalls(content string, calls []provider.ToolCall) {
	h.turns = append(h.turns, provider.Message{
		Role:      provider.RoleAssistant,
		Content:   content,
		ToolCalls: calls,
	})
}

// AppendToolResult records the outcome of one tool call.
//
// The id must be the one from the call being answered. Providers reject a tool
// result whose id matches no pending call, so a failed tool still has to append
// a result here — with the error as its content — rather than nothing at all.
func (h *History) AppendToolResult(id, content string) {
	h.turns = append(h.turns, provider.Message{
		Role:       provider.RoleTool,
		Content:    content,
		ToolCallID: id,
	})
}

// Turns returns a copy of the conversation turns, excluding the system prompt.
// The copy keeps callers from mutating history through the returned slice.
func (h *History) Turns() []provider.Message {
	out := make([]provider.Message, len(h.turns))
	copy(out, h.turns)
	for i := range out {
		out[i].ToolCalls = slices.Clone(out[i].ToolCalls)
	}
	return out
}

// Len reports the number of turns, excluding the system prompt.
func (h *History) Len() int { return len(h.turns) }

// Clear drops every turn but keeps the system prompt, which is what /clear
// means to a user: a fresh conversation with the same assistant.
func (h *History) Clear() { h.turns = nil }

// Messages returns the system prompt followed by every turn: the full,
// untrimmed conversation as a provider would receive it, with obvious secrets
// removed. Use Turns to read the conversation as the user wrote it.
func (h *History) Messages() []provider.Message {
	return redactOutbound(h.messages())
}

// messages assembles the conversation verbatim, before redaction.
func (h *History) messages() []provider.Message {
	out := make([]provider.Message, 0, len(h.turns)+1)
	if h.system != "" {
		out = append(out, provider.Message{Role: provider.RoleSystem, Content: h.system})
	}
	return append(out, h.turns...)
}

// Prompt builds the message list to send, trimmed to fit budget tokens.
//
// The system prompt is always kept and the newest turns are preferred: context
// is dropped from the oldest end, which is the part the model is least likely
// to need. When even the newest turn cannot fit, it is sent anyway and the
// provider decides — refusing locally would leave the user unable to ask
// anything at all, and providers report an over-long prompt clearly.
//
// Secrets are removed before trimming rather than after, so the budget is
// measured against the text that will actually be sent.
func (h *History) Prompt(budget int) []provider.Message {
	msgs := h.Messages()
	if budget <= 0 || TotalTokens(msgs) <= budget {
		return msgs
	}

	var head, turns []provider.Message
	if h.system != "" {
		head, turns = msgs[:1], msgs[1:]
	} else {
		turns = msgs
	}
	remaining := budget - TotalTokens(head)

	// Walk backwards from the newest turn, keeping what fits.
	keep := len(turns)
	for i := len(turns) - 1; i >= 0; i-- {
		cost := messageTokens(turns[i])
		if cost > remaining {
			break
		}
		remaining -= cost
		keep = i
	}

	if keep == len(turns) && len(turns) > 0 {
		// Not even the newest turn fits. Send it regardless.
		keep = len(turns) - 1
	}
	return append(head, turns[firstWholeTurn(turns, keep):]...)
}

// firstWholeTurn moves keep forward past any tool result whose originating
// call was trimmed away.
//
// A tool result only means something next to the call it answers, and
// providers reject a result whose id matches no call in the request. Trimming
// from the oldest end can cut between the two, so the window is advanced until
// it starts on a turn that stands alone. Dropping a little more context is the
// cheap failure here; sending an orphaned result fails the request outright.
func firstWholeTurn(turns []provider.Message, keep int) int {
	for keep < len(turns) && turns[keep].Role == provider.RoleTool {
		keep++
	}
	return keep
}

// redactOutbound copies msgs with every secret removed from the content and
// tool arguments.
//
// The copy is the point: local history stays exactly as the user typed it, so
// the transcript on screen still shows what they pasted, while the bytes that
// leave the machine do not carry the credential. Tool arguments are covered
// too — a model asked to write a config file puts the value it was given
// there, and that request travels to the provider like any other.
//
// The conservative [redact.Text] rule is used, never the aggressive one the
// provider package applies to error messages: on conversation content the
// latter would swallow commit hashes, UUIDs and long identifiers, and guessing
// wrong in that direction corrupts the question being asked.
func redactOutbound(msgs []provider.Message) []provider.Message {
	if len(msgs) == 0 {
		return msgs
	}

	out := make([]provider.Message, len(msgs))
	copy(out, msgs)
	for i := range out {
		out[i].Content = redact.Text(out[i].Content)
		if len(out[i].ToolCalls) == 0 {
			continue
		}
		calls := make([]provider.ToolCall, len(out[i].ToolCalls))
		copy(calls, out[i].ToolCalls)
		for j := range calls {
			calls[j].Arguments = redact.Text(calls[j].Arguments)
		}
		out[i].ToolCalls = calls
	}
	return out
}
