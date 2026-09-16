package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"yonderllm/internal/perm"
	"yonderllm/internal/provider"
	"yonderllm/internal/session"
)

func TestSaveCommandAndResumedTranscript(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	store, err := session.OpenSessions(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.sessions = store
	drainExchange(t, &m, "original question")
	m = typing(m, "/save project")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(transcript(m), "saved conversation project") {
		t.Fatal(transcript(m))
	}
	conv, err := store.Get("project")
	if err != nil {
		t.Fatal(err)
	}
	next := newTestModel(t, &stubProvider{name: "stub"})
	if err := next.sess.LoadConversation(conv); err != nil {
		t.Fatal(err)
	}
	next = newModelWithSessions(next.sess, perm.Chat, nil, store)
	if !strings.Contains(transcript(next), "original question") {
		t.Fatal("restored history is missing from transcript")
	}
	if next.asking || next.busy || next.mode != perm.Chat {
		t.Fatal("resume restored execution or approval state")
	}
}

func TestCompletedTurnAutosavesButClearStartsFresh(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub", deltas: []string{"answer"}})
	store, err := session.OpenSessions(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.sess.EnableSaving(store, ""); err != nil {
		t.Fatal(err)
	}
	drainExchange(t, &m, "first")
	conv, err := store.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if len(conv.Messages) != 2 || conv.Messages[1].Role != provider.RoleAssistant {
		t.Fatalf("autosaved = %+v", conv)
	}
	m.clearSession()
	drainExchange(t, &m, "second")
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("clear overwrote old conversation: %d files", len(list))
	}
	conv, err = store.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if len(conv.Messages) != 2 || conv.Messages[0].Content != "second" {
		t.Fatalf("post-clear autosave = %+v", conv)
	}
}
