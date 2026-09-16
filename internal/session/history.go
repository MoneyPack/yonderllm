// Package session owns a conversation: the running history, the trimming that
// keeps it inside a model's context window, token accounting against the daily
// cap, and the fallback across configured providers when one runs out of quota.
//
// Inference is remote, so the expensive resource this package protects is not
// CPU but the free-tier request and token budget.
package session

import (
	"slices"
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

// placeholder is what a removed credential leaves behind. It is deliberately
// visible: a model that receives it can say "you redacted that" instead of
// silently reasoning about a value that is no longer there.
const placeholder = "[redacted]"

// bearerMarker introduces a credential in an Authorization header, which is
// the most common way one arrives in a prompt: pasted along with a curl
// command the user wants explained.
const bearerMarker = "bearer "

// redactable removes obvious secrets from a piece of text.
//
// Three shapes are recognised, each a strong signal on its own: a value
// following a bearer marker, a token carrying a known vendor key prefix, and
// the right-hand side of an assignment whose name says it holds a credential.
// Everything else is passed through byte for byte, including runs of
// whitespace: this text is going to a model, and collapsing the layout of a
// pasted file would cost more in comprehension than it saves.
//
// Deliberately absent is the "long opaque run of characters" rule the provider
// package uses on error messages. That rule is safe on a short sentence from an
// API but not on conversation content, where it would swallow commit hashes,
// UUIDs and any sufficiently long identifier. Guessing wrong in that direction
// corrupts the question being asked, so obvious secrets are caught and
// ambiguous ones are left alone.
func redactable(s string) string {
	if s == "" {
		return s
	}

	var b strings.Builder
	written := 0
	replace := func(start, end int, value string) {
		if b.Cap() == 0 {
			b.Grow(len(s))
		}
		b.WriteString(s[written:start])
		b.WriteString(value)
		written = end
	}

	// Set once an assignment has named a secret but its value has not been
	// reached yet, which happens when the value is quoted: KEY="..." puts a
	// delimiter between the name and the thing to remove.
	valueIsSecret := false

	for i := 0; i < len(s); {
		if n := bearerAt(s, i); n > 0 {
			end := endOfToken(s, i+n)
			replace(i+n, end, placeholder)
			i = end
			valueIsSecret = false
			continue
		}

		if isTokenEnd(s[i]) {
			// Only a quote may stand between a secret's name and its value.
			// Anything else means the value never arrived.
			if !isQuote(s[i]) {
				valueIsSecret = false
			}
			i++
			continue
		}

		end := endOfToken(s, i)
		if valueIsSecret {
			replace(i, end, placeholder)
			valueIsSecret = false
		} else {
			text, expectValue := redactToken(s[i:end])
			if text != s[i:end] {
				replace(i, end, text)
			}
			valueIsSecret = expectValue
		}
		i = end
	}

	if written == 0 {
		return s
	}
	b.WriteString(s[written:])
	return b.String()
}

// redactToken rewrites one whitespace-delimited token, reporting whether the
// secret it names is still to come in the next token.
func redactToken(token string) (text string, valueFollows bool) {
	if eq := strings.IndexByte(token, '='); eq >= 0 {
		name, value := token[:eq], token[eq+1:]
		if !namesSecret(name) {
			return token, false
		}
		if value == "" {
			// KEY= — the value is quoted, so it is the next token.
			return token, true
		}
		return name + "=" + placeholder, false
	}

	lead, core, trail := trimPunct(token)
	if looksLikeSecret(core) {
		return lead + placeholder + trail, false
	}
	return token, false
}

// namesSecret reports whether an assignment's left-hand side declares a
// credential, as the names in a .env file do.
//
// The match is on whole words so that ordinary prose keeps its meaning, and it
// is generous about which words count: removing a value that turns out to be
// harmless costs the model a little context, while keeping one that turns out
// to be a key hands it to a third party permanently.
func namesSecret(name string) bool {
	if name == "" || !isEnvName(name) {
		return false
	}
	upper := strings.ToUpper(name)
	for _, word := range []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL"} {
		if strings.HasSuffix(upper, word) {
			return true
		}
		for _, part := range strings.FieldsFunc(upper, func(r rune) bool { return r == '_' || r == '.' || r == '-' }) {
			if part == word {
				return true
			}
		}
	}
	return false
}

// isEnvName reports whether name is shaped like an environment variable, which
// keeps the assignment rule away from expressions and comparisons in code.
func isEnvName(name string) bool {
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// looksLikeSecret reports whether a bare token is recognisably an API key.
//
// A vendor prefix alone is not enough: "sk-" also begins plenty of ordinary
// words once a hyphen is involved. Requiring the length a real key has keeps
// the rule from firing on prose that merely mentions one.
func looksLikeSecret(s string) bool {
	const minKeyLen = 16

	if len(s) < minKeyLen {
		return false
	}
	prefixed := false
	for _, prefix := range []string{"sk-", "sk_", "gsk_", "AIza", "or-"} {
		if strings.HasPrefix(s, prefix) {
			prefixed = true
			break
		}
	}
	if !prefixed {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// trimPunct splits the punctuation a token picked up from the sentence around
// it away from the token itself, so that a redaction can replace the value
// without eating the comma after it.
func trimPunct(token string) (lead, core, trail string) {
	const (
		leading  = "([{<"
		trailing = ".,;:!?)]}>"
	)
	core = strings.TrimLeft(token, leading)
	lead = token[:len(token)-len(core)]
	trimmed := strings.TrimRight(core, trailing)
	trail, core = core[len(trimmed):], trimmed
	return lead, core, trail
}

// bearerAt reports the length of a bearer marker starting at i, or zero if
// there is none. The comparison ignores case because the header is written
// every way by hand.
func bearerAt(s string, i int) int {
	if len(s)-i < len(bearerMarker) {
		return 0
	}
	if !strings.EqualFold(s[i:i+len(bearerMarker)], bearerMarker) {
		return 0
	}
	return len(bearerMarker)
}

// endOfToken returns the index just past the token starting at i.
func endOfToken(s string, i int) int {
	for i < len(s) && !isTokenEnd(s[i]) {
		i++
	}
	return i
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

// isQuote reports whether b is one of the quotes isTokenEnd treats as a
// delimiter.
func isQuote(b byte) bool {
	switch b {
	case '\'', '"', '`':
		return true
	}
	return false
}

// redactOutbound copies msgs with every secret removed from the content and
// tool arguments.
//
// The copy is the point: local history stays exactly as the user typed it, so
// the transcript on screen still shows what they pasted, while the bytes that
// leave the machine do not carry the credential. Tool arguments are covered
// too — a model asked to write a config file puts the value it was given
// there, and that request travels to the provider like any other.
func redactOutbound(msgs []provider.Message) []provider.Message {
	if len(msgs) == 0 {
		return msgs
	}

	out := make([]provider.Message, len(msgs))
	copy(out, msgs)
	for i := range out {
		out[i].Content = redactable(out[i].Content)
		if len(out[i].ToolCalls) == 0 {
			continue
		}
		calls := make([]provider.ToolCall, len(out[i].ToolCalls))
		copy(calls, out[i].ToolCalls)
		for j := range calls {
			calls[j].Arguments = redactable(calls[j].Arguments)
		}
		out[i].ToolCalls = calls
	}
	return out
}
