package session

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"yonderllm/internal/provider"
)

func TestSessionsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSessions(dir)
	if err != nil {
		t.Fatalf("OpenSessions: %v", err)
	}

	conv := SavedConversation{
		Name:     "trip",
		Provider: "groq",
		Model:    "llama-3.1",
		System:   "you are terse",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "hi"},
			{Role: provider.RoleAssistant, Content: "hello"},
		},
	}
	if err := s.Save("trip", conv); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Get("trip")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Provider != "groq" || got.Model != "llama-3.1" || got.System != "you are terse" {
		t.Errorf("restored header = provider=%q model=%q system=%q", got.Provider, got.Model, got.System)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("restored %d messages, want 2", len(got.Messages))
	}
	if got.Messages[0].Role != provider.RoleUser || got.Messages[0].Content != "hi" {
		t.Errorf("message 0 = %+v", got.Messages[0])
	}
	if got.Messages[1].Role != provider.RoleAssistant || got.Messages[1].Content != "hello" {
		t.Errorf("message 1 = %+v", got.Messages[1])
	}
}

func TestSessionsRedactEveryContentSurfaceWithoutMutatingHistory(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	const key = "sk-abcdefghijklmnopqrstuv"
	conv := SavedConversation{System: "SECRET=" + key, Messages: []provider.Message{
		{Role: provider.RoleAssistant, Content: key, ToolCalls: []provider.ToolCall{{ID: "call", Name: "write_file", Arguments: `{"password":"unprefixed-secret","nested":{"token":"another-secret"},"text":"` + key + `"}`}}},
		{Role: provider.RoleTool, ToolCallID: "call", Content: "TOKEN=" + key},
	}}
	if err := store.Save("secrets", conv); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{key, "unprefixed-secret", "another-secret"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("stored secret %q", secret)
		}
	}
	if conv.Messages[0].Content != key || !strings.Contains(conv.Messages[0].ToolCalls[0].Arguments, "unprefixed-secret") {
		t.Fatal("saving mutated live history")
	}
}

func TestSessionsRejectIncompleteToolsWithoutReplacingSnapshot(t *testing.T) {
	store, err := OpenSessions(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("valid", SavedConversation{Messages: []provider.Message{{Role: provider.RoleUser, Content: "keep"}}}); err != nil {
		t.Fatal(err)
	}
	err = store.Save("valid", SavedConversation{Messages: []provider.Message{{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "pending", Name: "run_command", Arguments: `{}`}}}}})
	if err == nil {
		t.Fatal("incomplete call accepted")
	}
	conv, err := store.Get("valid")
	if err != nil {
		t.Fatal(err)
	}
	if conv.Messages[0].Content != "keep" {
		t.Fatal("invalid save replaced snapshot")
	}
}

func TestSessionsConcurrentWritersLeaveCompleteSnapshots(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for _, text := range []string{"one", "two", "three", "four"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store, err := OpenSessions(dir)
			if err != nil {
				t.Error(err)
				return
			}
			if err := store.Save("shared", SavedConversation{Messages: []provider.Message{{Role: provider.RoleUser, Content: text}, {Role: provider.RoleAssistant, Content: text}}}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	store, err := OpenSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := store.Get("shared")
	if err != nil {
		t.Fatal(err)
	}
	if len(conv.Messages) != 2 || conv.Messages[0].Content != conv.Messages[1].Content {
		t.Fatalf("torn snapshot: %+v", conv)
	}
}

func TestSessionsPreservesToolCalls(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenSessions(dir)
	conv := SavedConversation{
		System: "SECRET=system-secret",
		Messages: []provider.Message{
			{
				Role:    provider.RoleAssistant,
				Content: "reading",
				ToolCalls: []provider.ToolCall{
					{ID: "call_1", Name: "read_file", Arguments: `{"path":"a.go"}`},
				},
			},
			{Role: provider.RoleTool, Content: "package a", ToolCallID: "call_1"},
		},
	}
	if err := s.Save("tools", conv); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Get("tools")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Messages[0].ToolCalls) != 1 {
		t.Fatalf("tool call lost on round-trip: %+v", got.Messages[0].ToolCalls)
	}
	if got.Messages[0].ToolCalls[0].ID != "call_1" || got.Messages[0].ToolCalls[0].Name != "read_file" {
		t.Errorf("tool call = %+v", got.Messages[0].ToolCalls[0])
	}
	if got.Messages[1].ToolCallID != "call_1" {
		t.Errorf("tool result id = %q, want call_1", got.Messages[1].ToolCallID)
	}
}

func TestSessionsRedactsAtRest(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenSessions(dir)
	conv := SavedConversation{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "use sk-1234567890123456; store GROQ_API_KEY=sk-abcdefghijklmnopqrstuvwxyz"},
		},
	}
	if err := s.Save("secrets", conv); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The stored file must not contain the key anywhere.
	data, err := os.ReadFile(filepath.Join(dir, "secrets.json"))
	if err != nil {
		t.Fatalf("reading stored file: %v", err)
	}
	if strings.Contains(string(data), "sk-1234567890123456") {
		t.Error("raw key present in the stored conversation file")
	}
	if strings.Contains(string(data), "sk-abcdefghijklmnopqrstuvwxyz") {
		t.Error("raw key value present in the stored conversation file")
	}

	// Loading back must present the redacted form.
	got, _ := s.Get("secrets")
	if strings.Contains(got.Messages[0].Content, "1234567890123456") {
		t.Errorf("loaded message still carries the key: %q", got.Messages[0].Content)
	}
}

func TestSessionsNameSanitization(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenSessions(dir)
	if err := s.Save("../escape", SavedConversation{Messages: []provider.Message{{Role: provider.RoleUser, Content: "x"}}}); err == nil {
		t.Fatal("hostile name accepted")
	}
	// The name resolving to "." would collide with the directory itself; a
	// sanitized name must stay inside the sessions dir.
	if _, err := s.Get("../escape"); err == nil {
		t.Fatal("hostile lookup accepted")
	}
	// A name that sanitizes to empty (the root) must be refused.
	if err := s.Save("....", SavedConversation{}); err == nil {
		t.Error("name sanitizing to nothing was accepted")
	}
}

func TestSessionsRejectsUnknownVersion(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenSessions(dir)
	if err := s.Save("v1", SavedConversation{Messages: []provider.Message{{Role: provider.RoleUser, Content: "x"}}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Rewrite the file with a future version, as a newer tool would.
	path := filepath.Join(dir, "v1.json")
	data, _ := os.ReadFile(path)
	rewritten := strings.Replace(string(data), `"version": 1`, `"version": 99`, 1)
	if err := os.WriteFile(path, []byte(rewritten), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("v1"); err == nil {
		t.Error("future-version conversation was read instead of refused")
	}
}

func TestSessionsListAndDelete(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenSessions(dir)
	for _, name := range []string{"alpha", "beta"} {
		if err := s.Save(name, SavedConversation{Messages: []provider.Message{{Role: provider.RoleUser, Content: name}}}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List returned %d, want 2", len(list))
	}
	if list[0].Name != "beta" || list[1].Name != "alpha" {
		t.Errorf("List order = %q, %q; want beta, alpha", list[0].Name, list[1].Name)
	}

	if err := s.Delete("alpha"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get("alpha"); err == nil {
		t.Error("deleted conversation still loads")
	}
	// Deleting a name that isn't there must fail loudly.
	if err := s.Delete("alpha"); err == nil {
		t.Error("deleting a missing conversation succeeded")
	}
}
