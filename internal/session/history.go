// Package session owns a conversation: the running history, the trimming that
// keeps it inside a model's context window, token accounting against the daily
// cap, and the fallback across configured providers when one runs out of quota.
//
// Inference is remote, so the expensive resource this package protects is not
// CPU but the free-tier request and token budget.
package session

import (
	"strings"
	"unicode/utf8"

	"yonderllm/internal/provider"
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
func messageTokens(m provider.Message) int {
	return EstimateTokens(m.Content) + tokensPerMessage
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

// Append adds a turn to the end of the conversation.
func (h *History) Append(role provider.Role, content string) {
	h.turns = append(h.turns, provider.Message{Role: role, Content: content})
}

// Turns returns a copy of the conversation turns, excluding the system prompt.
// The copy keeps callers from mutating history through the returned slice.
func (h *History) Turns() []provider.Message {
	out := make([]provider.Message, len(h.turns))
	copy(out, h.turns)
	return out
}

// Len reports the number of turns, excluding the system prompt.
func (h *History) Len() int { return len(h.turns) }

// Clear drops every turn but keeps the system prompt, which is what /clear
// means to a user: a fresh conversation with the same assistant.
func (h *History) Clear() { h.turns = nil }

// Messages returns the system prompt followed by every turn: the full,
// untrimmed conversation as a provider would receive it.
func (h *History) Messages() []provider.Message {
	var out []provider.Message
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
func (h *History) Prompt(budget int) []provider.Message {
	msgs := h.Messages()
	if budget <= 0 || TotalTokens(msgs) <= budget {
		return msgs
	}

	var head []provider.Message
	if h.system != "" {
		head = []provider.Message{{Role: provider.RoleSystem, Content: h.system}}
	}
	remaining := budget - TotalTokens(head)

	// Walk backwards from the newest turn, keeping what fits.
	keep := len(h.turns)
	for i := len(h.turns) - 1; i >= 0; i-- {
		cost := messageTokens(h.turns[i])
		if cost > remaining {
			break
		}
		remaining -= cost
		keep = i
	}

	if keep == len(h.turns) && len(h.turns) > 0 {
		// Not even the newest turn fits. Send it regardless.
		keep = len(h.turns) - 1
	}
	return append(head, h.turns[keep:]...)
}

// redactable is a small guard used when history is rendered for display or
// logs: it strips anything that looks like a bearer credential pasted into a
// prompt, so a transcript written to disk cannot leak one.
func redactable(s string) string {
	const marker = "Bearer "
	idx := strings.Index(s, marker)
	if idx < 0 {
		return s
	}
	end := idx + len(marker)
	for end < len(s) && !isTokenEnd(s[end]) {
		end++
	}
	return s[:idx+len(marker)] + "[redacted]" + s[end:]
}

// isTokenEnd reports whether b closes a credential token. Quotes count as
// delimiters, not token characters: credentials never contain them, and
// consuming a closing quote would corrupt the surrounding text it belongs to.
func isTokenEnd(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\'', '"', '`':
		return true
	}
	return false
}
