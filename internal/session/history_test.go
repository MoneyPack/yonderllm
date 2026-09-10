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
	}{
		{"no credential", "just a prompt", "just a prompt"},
		{"token mid-string", "curl -H 'Bearer sk-secret' url", "curl -H 'Bearer [redacted]' url"},
		{"token at end", "use Bearer sk-secret", "use Bearer [redacted]"},
		{"token before newline", "Bearer sk-secret\nnext line", "Bearer [redacted]\nnext line"},
		{"bare marker", "Bearer ", "Bearer [redacted]"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactable(c.in)
			if got != c.want {
				t.Errorf("redactable(%q) = %q, want %q", c.in, got, c.want)
			}
			if strings.Contains(got, "sk-secret") {
				t.Errorf("redactable(%q) leaked the credential: %q", c.in, got)
			}
		})
	}
}
