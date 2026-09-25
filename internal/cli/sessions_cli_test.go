package cli

import (
	"testing"

	"github.com/MoneyPack/yonderllm/internal/provider"
	"github.com/MoneyPack/yonderllm/internal/session"
)

func TestSaveResumeLastAndDelete(t *testing.T) {
	h := newHarness(t, newStub(t, "hello"))
	wantCode(t, h.run(t, "ask", "--save", "project", "first question"), 0)
	wantContains(t, "list", h.run(t, "sessions").stdout, "project")
	r := h.run(t, "run", "--resume", "project", "--no-save", "next question")
	wantCode(t, r, 0)
	msgs := h.stub.last.Messages
	if len(msgs) != 3 || msgs[0].Content != "first question" || msgs[1].Content != "hello" || msgs[2].Content != "next question" {
		t.Fatalf("resume context = %+v", msgs)
	}
	wantCode(t, h.run(t, "ask", "--last", "--no-save", "third question"), 0)
	if len(h.stub.last.Messages) != 3 {
		t.Fatal("--no-save changed the stored conversation")
	}
	wantCode(t, h.run(t, "sessions", "delete", "project"), 0)
	wantCode(t, h.run(t, "ask", "--last", "gone"), 1)
}

func TestResumeRestoresModelAndAllowsOverride(t *testing.T) {
	h := newHarness(t, newStub(t, "hello"))
	wantCode(t, h.run(t, "ask", "--model", "saved-model", "--save", "model-test", "hello"), 0)
	wantCode(t, h.run(t, "ask", "--resume", "model-test", "--no-save", "again"), 0)
	if h.stub.last.Model != "saved-model" {
		t.Errorf("model = %s", h.stub.last.Model)
	}
	wantCode(t, h.run(t, "ask", "--resume", "model-test", "--model", "override", "--no-save", "again"), 0)
	if h.stub.last.Model != "override" {
		t.Errorf("override model = %s", h.stub.last.Model)
	}
}

func TestResumeRejectsUnknownProviderBeforeNetwork(t *testing.T) {
	h := newHarness(t, newStub(t, "hello"))
	s, err := openSessions()
	if err != nil {
		t.Fatal(err)
	}
	err = s.Save("missing", session.SavedConversation{Provider: "unconfigured", Model: "model", Messages: []provider.Message{{Role: provider.RoleUser, Content: "old"}}})
	if err != nil {
		t.Fatal(err)
	}
	wantCode(t, h.run(t, "ask", "--resume", "missing", "next"), 1)
	if h.stub.seen {
		t.Fatal("invalid resume contacted provider")
	}
}

func TestSessionFlagsRejectConflicts(t *testing.T) {
	h := newHarness(t, newStub(t, "hello"))
	for _, args := range [][]string{{"ask", "--resume", "one", "--last", "hi"}, {"ask", "--save", "one", "--no-save", "hi"}, {"sessions", "unexpected"}} {
		wantCode(t, h.run(t, args...), 1)
	}
	if h.stub.seen {
		t.Fatal("invalid flags contacted provider")
	}
}
