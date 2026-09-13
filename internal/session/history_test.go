package session

import (
	"strings"
	"testing"

	"yonderllm/internal/provider"
)

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"one rune", "a", 1},
		{"exactly one token", "abcd", 1},
		{"rounds up", "abcde", 2},
		{"counts runes not bytes", "héllo wörld", 3},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EstimateTokens(c.in); got != c.want {
				t.Errorf("EstimateTokens(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestTotalTokensAddsFramingPerMessage(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "abcd"},      // 1 + 4
		{Role: provider.RoleAssistant, Content: "efgh"}, // 1 + 4
	}
	if got, want := TotalTokens(msgs), 10; got != want {
		t.Errorf("TotalTokens = %d, want %d", got, want)
	}
	if got, want := TotalTokens(nil), 0; got != want {
		t.Errorf("TotalTokens(nil) = %d, want %d", got, want)
	}
}

func TestTotalTokensCountsToolCalls(t *testing.T) {
	// An assistant turn that only asks to run tools carries no text, so the
	// whole cost of the turn lives in the call names and arguments.
	msgs := []provider.Message{{
		Role: provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{
			{ID: "1", Name: "read", Arguments: `{"path":"a"}`}, // 1 + 3 + 4
			{ID: "2", Name: "grep", Arguments: `{}`},           // 1 + 1 + 4
		},
	}}
	// 0 content + 4 framing, then 8 and 6 for the calls.
	if got, want := TotalTokens(msgs), 18; got != want {
		t.Errorf("TotalTokens of a tool-call turn = %d, want %d", got, want)
	}

	bare := []provider.Message{{Role: provider.RoleAssistant}}
	if TotalTokens(msgs) <= TotalTokens(bare) {
		t.Error("tool calls cost nothing: a long call chain would slip past the budget uncounted")
	}
}

func TestHistorySystemPromptIsSeparateFromTurns(t *testing.T) {
	var h History
	h.SetSystem("be terse")
	h.Append(provider.RoleUser, "hello")

	if got, want := h.System(), "be terse"; got != want {
		t.Errorf("System() = %q, want %q", got, want)
	}
	if got, want := h.Len(), 1; got != want {
		t.Errorf("Len() = %d, want %d; the system prompt must not count as a turn", got, want)
	}

	msgs := h.Messages()
	if len(msgs) != 2 {
		t.Fatalf("Messages() returned %d messages, want 2", len(msgs))
	}
	if msgs[0].Role != provider.RoleSystem {
		t.Errorf("Messages()[0].Role = %q, want %q", msgs[0].Role, provider.RoleSystem)
	}
	if msgs[1].Role != provider.RoleUser {
		t.Errorf("Messages()[1].Role = %q, want %q", msgs[1].Role, provider.RoleUser)
	}
}

func TestHistoryMessagesOmitsEmptySystemPrompt(t *testing.T) {
	var h History
	h.Append(provider.RoleUser, "hello")

	msgs := h.Messages()
	if len(msgs) != 1 {
		t.Fatalf("Messages() returned %d messages, want 1", len(msgs))
	}
	if msgs[0].Role != provider.RoleUser {
		t.Errorf("Messages()[0].Role = %q, want %q", msgs[0].Role, provider.RoleUser)
	}
}

func TestHistoryTurnsReturnsACopy(t *testing.T) {
	var h History
	h.Append(provider.RoleUser, "original")

	turns := h.Turns()
	turns[0].Content = "tampered"

	if got, want := h.Turns()[0].Content, "original"; got != want {
		t.Errorf("history was mutated through the returned slice: got %q, want %q", got, want)
	}
}

func TestHistoryClearKeepsSystemPrompt(t *testing.T) {
	var h History
	h.SetSystem("be terse")
	h.Append(provider.RoleUser, "hello")
	h.Append(provider.RoleAssistant, "hi")

	h.Clear()

	if got, want := h.Len(), 0; got != want {
		t.Errorf("Len() after Clear = %d, want %d", got, want)
	}
	if got, want := h.System(), "be terse"; got != want {
		t.Errorf("System() after Clear = %q, want %q", got, want)
	}
}

// promptFixture builds a history whose token costs are exact and easy to reason
// about: the system prompt costs 5 tokens and every turn costs 5.
func promptFixture(t *testing.T, system bool) *History {
	t.Helper()

	h := &History{}
	if system {
		h.SetSystem("sys") // 1 token + 4 framing
	}
	h.Append(provider.RoleUser, "aaaa")      // 5
	h.Append(provider.RoleAssistant, "bbbb") // 5
	h.Append(provider.RoleUser, "cccc")      // 5
	return h
}

func contents(msgs []provider.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
}

func TestHistoryPrompt(t *testing.T) {
	cases := []struct {
		name   string
		system bool
		budget int
		want   []string
	}{
		{"zero budget sends everything", true, 0, []string{"sys", "aaaa", "bbbb", "cccc"}},
		{"negative budget sends everything", true, -1, []string{"sys", "aaaa", "bbbb", "cccc"}},
		{"exact fit sends everything", true, 20, []string{"sys", "aaaa", "bbbb", "cccc"}},
		{"drops the oldest turn first", true, 19, []string{"sys", "bbbb", "cccc"}},
		{"keeps only the newest turn", true, 10, []string{"sys", "cccc"}},
		{"oversized newest turn is sent anyway", true, 6, []string{"sys", "cccc"}},
		{"system prompt survives an impossible budget", true, 1, []string{"sys", "cccc"}},
		{"trims without a system prompt", false, 6, []string{"cccc"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := promptFixture(t, c.system)
			got := contents(h.Prompt(c.budget))

			if len(got) != len(c.want) {
				t.Fatalf("Prompt(%d) = %v, want %v", c.budget, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("Prompt(%d) = %v, want %v", c.budget, got, c.want)
				}
			}
		})
	}
}

func TestHistoryPromptOnEmptyHistory(t *testing.T) {
	var h History
	if got := h.Prompt(10); len(got) != 0 {
		t.Errorf("Prompt on empty history = %v, want no messages", contents(got))
	}
}

func TestRedactable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		// leak is the substring that must not survive. Empty means the case
		// carries no credential and is here to prove nothing was removed.
		leak string
	}{
		{name: "no credential", in: "just a prompt", want: "just a prompt"},
		{
			name: "token mid-string",
			in:   "curl -H 'Bearer sk-secret' url",
			want: "curl -H 'Bearer [redacted]' url",
			leak: "sk-secret",
		},
		{
			name: "token at end",
			in:   "use Bearer sk-secret",
			want: "use Bearer [redacted]",
			leak: "sk-secret",
		},
		{
			name: "token before newline",
			in:   "Bearer sk-secret\nnext line",
			want: "Bearer [redacted]\nnext line",
			leak: "sk-secret",
		},
		{name: "bare marker", in: "Bearer ", want: "Bearer [redacted]"},
		{
			name: "vendor key alone",
			in:   "sk-0123456789abcdef",
			want: "[redacted]",
			leak: "sk-0123456789abcdef",
		},
		{
			name: "vendor key in a sentence",
			in:   "is gsk_0123456789abcdef still valid?",
			want: "is [redacted] still valid?",
			leak: "gsk_0123456789abcdef",
		},
		{
			name: "env assignment",
			in:   "GROQ_API_KEY=gsk_0123456789abcdef",
			want: "GROQ_API_KEY=[redacted]",
			leak: "gsk_0123456789abcdef",
		},
		{
			name: "quoted env assignment",
			in:   `GEMINI_API_KEY="averysecretvalue"`,
			want: `GEMINI_API_KEY="[redacted]"`,
			leak: "averysecretvalue",
		},
		{
			name: "layout is preserved byte for byte",
			in:   "# .env\n\n\tOPENROUTER_API_KEY=or-0123456789abcdef\n",
			want: "# .env\n\n\tOPENROUTER_API_KEY=[redacted]\n",
			leak: "or-0123456789abcdef",
		},
		// The rules below must not fire: guessing wrong here corrupts the
		// question the user is asking.
		{name: "short mention of a prefix", in: "sk-1 is too short", want: "sk-1 is too short"},
		{name: "unrelated assignment", in: "PATH=/usr/local/bin", want: "PATH=/usr/local/bin"},
		{
			name: "commit hash",
			in:   "revert 9f4a1c2d3e5b6a7f8c9d0e1f2a3b4c5d6e7f8a9b",
			want: "revert 9f4a1c2d3e5b6a7f8c9d0e1f2a3b4c5d6e7f8a9b",
		},
		{
			name: "uuid",
			in:   "row 123e4567-e89b-12d3-a456-426614174000 is missing",
			want: "row 123e4567-e89b-12d3-a456-426614174000 is missing",
		},
		{
			name: "long identifier",
			in:   "rename configurationManagerFactoryProvider",
			want: "rename configurationManagerFactoryProvider",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactable(c.in)
			if got != c.want {
				t.Errorf("redactable(%q) = %q, want %q", c.in, got, c.want)
			}
			if c.leak != "" && strings.Contains(got, c.leak) {
				t.Errorf("redactable(%q) leaked the credential: %q", c.in, got)
			}
		})
	}
}

// outboundText collects everything a request would carry: message content plus
// the arguments of every tool call, which travel to the provider as well.
func outboundText(msgs []provider.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Content)
		b.WriteByte('\n')
		for _, c := range m.ToolCalls {
			b.WriteString(c.Arguments)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func TestOutboundMessagesDropSecrets(t *testing.T) {
	const secret = "gsk_0123456789abcdef"

	var h History
	h.SetSystem("you are helpful")
	h.Append(provider.RoleUser, "put GROQ_API_KEY="+secret+" in .env")
	h.AppendToolCalls("writing it now", []provider.ToolCall{{
		ID:        "call-1",
		Name:      "write_file",
		Arguments: `{"path":".env","body":"GROQ_API_KEY=` + secret + `"}`,
	}})
	h.AppendToolResult("call-1", "wrote .env")

	// Redaction happens before trimming, so every budget is covered.
	for _, budget := range []int{0, 40, 12} {
		if got := outboundText(h.Prompt(budget)); strings.Contains(got, secret) {
			t.Errorf("Prompt(%d) leaked the credential:\n%s", budget, got)
		}
	}
	if got := outboundText(h.Messages()); strings.Contains(got, secret) {
		t.Errorf("Messages leaked the credential:\n%s", got)
	}

	// The tool call keeps its shape around the removed value.
	msgs := h.Messages()
	call := msgs[2].ToolCalls[0]
	if want := `{"path":".env","body":"GROQ_API_KEY=[redacted]"}`; call.Arguments != want {
		t.Errorf("tool arguments = %q, want %q", call.Arguments, want)
	}
	if call.ID != "call-1" || call.Name != "write_file" {
		t.Errorf("redaction disturbed the call: %+v", call)
	}

	// Local history is untouched: the transcript still shows what was typed.
	turns := h.Turns()
	if !strings.Contains(turns[0].Content, secret) {
		t.Errorf("stored turn was redacted: %q", turns[0].Content)
	}
	if !strings.Contains(turns[1].ToolCalls[0].Arguments, secret) {
		t.Errorf("stored tool arguments were redacted: %q", turns[1].ToolCalls[0].Arguments)
	}
}

// toolTurnFixture builds a history whose middle holds a tool exchange: a user
// question, the assistant's call, the result answering it, then a plain turn
// either side. Costs are 5, 10, 5, 5, 5 tokens for a total of 30.
func toolTurnFixture(t *testing.T) *History {
	t.Helper()

	var h History
	h.Append(provider.RoleUser, "aaaa")
	h.AppendToolCalls("", []provider.ToolCall{{ID: "call-1", Name: "read", Arguments: "{}"}})
	h.AppendToolResult("call-1", "rrrr")
	h.Append(provider.RoleUser, "bbbb")
	h.Append(provider.RoleAssistant, "cccc")

	if got, want := TotalTokens(h.Turns()), 30; got != want {
		t.Fatalf("fixture cost = %d tokens, want %d", got, want)
	}
	return &h
}

func TestHistoryPromptNeverOrphansAToolResult(t *testing.T) {
	h := toolTurnFixture(t)

	// A budget of 16 fits the three newest turns, which would cut between the
	// call and the result that answers it.
	got := h.Prompt(16)
	if len(got) != 2 {
		t.Fatalf("Prompt(16) returned %d messages (%v), want 2", len(got), contents(got))
	}
	if got[0].Role != provider.RoleUser || got[0].Content != "bbbb" {
		t.Errorf("Prompt(16) starts with %s %q, want user \"bbbb\"", got[0].Role, got[0].Content)
	}
}

func TestHistoryPromptKeepsResultWithItsCall(t *testing.T) {
	h := toolTurnFixture(t)

	// A budget of 25 fits everything but the oldest user turn, so the call and
	// its result both survive.
	got := h.Prompt(25)
	if len(got) != 4 {
		t.Fatalf("Prompt(25) returned %d messages (%v), want 4", len(got), contents(got))
	}
	if len(got[0].ToolCalls) != 1 {
		t.Fatalf("Prompt(25) starts with %+v, want the assistant tool call", got[0])
	}
	if got[1].Role != provider.RoleTool || got[1].ToolCallID != "call-1" {
		t.Errorf("call is not followed by its result: got %+v", got[1])
	}
}

func TestHistoryPromptLeadingToolResultInvariant(t *testing.T) {
	h := toolTurnFixture(t)
	h.SetSystem("sys")

	// Whatever the budget, the first turn sent must never be a dangling tool
	// result: providers reject a result whose id matches no call in the request.
	for budget := 1; budget <= 40; budget++ {
		msgs := h.Prompt(budget)
		for _, m := range msgs {
			if m.Role == provider.RoleSystem {
				continue
			}
			if m.Role == provider.RoleTool {
				t.Errorf("Prompt(%d) leads with an orphaned tool result: %v", budget, contents(msgs))
			}
			break
		}
	}
}
