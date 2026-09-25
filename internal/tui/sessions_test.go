package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/MoneyPack/yonderllm/internal/perm"
	"github.com/MoneyPack/yonderllm/internal/provider"
	"github.com/MoneyPack/yonderllm/internal/session"
)

func TestSaveCommandAndResumedTranscript(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	store, err := session.OpenSessions(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.sessions = store
	drainExchange(t, &m, "original question")
	m, async := command(t, m, "/save project")
	if !async {
		t.Fatal("/save wrote to disk on the message loop")
	}
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

// A /save that cannot be written reports the failure where the placeholder
// was, in the same shape as a success, and leaves the interface usable: the
// command is no longer running and nothing was left half-saved on disk. The
// two ways a save fails are exercised: a name the store refuses, which is
// decided before anything is written, and a store whose directory has gone.
func TestSaveFailureIsReportedInPlaceAndReleasesTheLoop(t *testing.T) {
	dir := t.TempDir()
	store, err := session.OpenSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, &stubProvider{name: "stub", deltas: []string{"answer"}})
	m.sessions = store
	drainExchange(t, &m, "keep me")

	// A name the store refuses. The check happens in the command, not on the
	// loop, so the placeholder still appears first.
	before := len(m.blocks)
	m, async := command(t, m, "/save Not-Lower")
	if !async {
		t.Fatal("/save with a bad name never went off the loop")
	}
	if len(m.blocks) != before+1 {
		t.Fatalf("blocks = %d after a failed save, want %d: the result should replace the placeholder", len(m.blocks), before+1)
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError || !strings.HasPrefix(last.text, "save failed: ") || !strings.Contains(last.text, "lowercase") {
		t.Fatalf("last block = %+v, want an error explaining the name", last)
	}
	if strings.Contains(transcript(m), "saving conversation Not-Lower…") {
		t.Fatalf("placeholder outlived the failure:\n%s", transcript(m))
	}
	if list, err := store.List(); err != nil || len(list) != 0 {
		t.Fatalf("a refused save left files behind: %v %v", list, err)
	}

	// The store's directory disappears after the model was built.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	m, async = command(t, m, "/save project")
	if !async {
		t.Fatal("/save with a missing store never went off the loop")
	}
	last = m.blocks[len(m.blocks)-1]
	if last.kind != blockError || !strings.HasPrefix(last.text, "save failed: ") {
		t.Fatalf("last block = %+v, want a save error", last)
	}
	if m.command != "" || m.busy {
		t.Fatalf("interface still held after a failed save: command=%q busy=%v", m.command, m.busy)
	}

	// The conversation itself is untouched by the failure, so a later save
	// to a working store still has it.
	fresh, err := session.OpenSessions(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.sessions = fresh
	m, _ = command(t, m, "/save project")
	if !strings.Contains(transcript(m), "saved conversation project") {
		t.Fatalf("save after failures did not succeed:\n%s", transcript(m))
	}
	conv, err := fresh.Get("project")
	if err != nil || len(conv.Messages) != 2 || conv.Messages[0].Content != "keep me" {
		t.Fatalf("saved conversation = %+v, %v", conv, err)
	}
}

// saveBlock is the part of /save that runs off the loop; its two outcomes are
// pinned directly so the message shapes are covered without a model.
func TestSaveBlockReportsBothOutcomes(t *testing.T) {
	store, err := session.OpenSessions(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	conv := session.SavedConversation{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}}
	if b := saveBlock(store, "ok", conv); b.kind != blockNotice || b.text != "saved conversation ok" {
		t.Errorf("saveBlock success = %+v", b)
	}
	if b := saveBlock(store, "..", conv); b.kind != blockError || !strings.HasPrefix(b.text, "save failed: ") {
		t.Errorf("saveBlock failure = %+v", b)
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
