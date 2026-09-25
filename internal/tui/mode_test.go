package tui

import (
	"context"
	"iter"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MoneyPack/yonderllm/internal/perm"
	"github.com/MoneyPack/yonderllm/internal/provider"
	"github.com/MoneyPack/yonderllm/internal/session"
	"github.com/MoneyPack/yonderllm/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

type modeWriteProvider struct{ stubProvider }

func (p *modeWriteProvider) Stream(ctx context.Context, req provider.Request) iter.Seq2[provider.Chunk, error] {
	return func(yield func(provider.Chunk, error) bool) {
		for _, m := range req.Messages {
			if m.Role == provider.RoleTool {
				yield(provider.Chunk{Delta: "finished", Finish: provider.FinishStop}, nil)
				return
			}
		}
		yield(provider.Chunk{Finish: provider.FinishTool, ToolCalls: []provider.ToolCall{{ID: "write", Name: "write_file", Arguments: `{"path":"mode-write.txt","content":"new content"}`}}}, nil)
	}
}

func TestModeSwitchDropsAutoApprovalAndHonorsDenial(t *testing.T) {
	workspaceDir(t, map[string]string{"mode-write.txt": "original"})
	p := &modeWriteProvider{stubProvider: stubProvider{name: "stub"}}
	sess := session.New(testConfig(), func(string) (provider.Provider, error) { return p, nil })
	approvals := NewApprovals()
	sess.SetTools(tools.For(perm.NewAutoApprove(perm.Agent), approvals.Ask)...)
	m := newModel(sess, perm.Agent, approvals)
	m.applyMode([]string{"chat"})
	m.applyMode([]string{"agent"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		for _, err := range sess.Ask(ctx, "write the file") {
			if err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()
	select {
	case req := <-approvals.ch:
		if req.action != perm.Write {
			t.Errorf("approval action = %s", req.action)
		}
		req.reply <- false
	case err := <-finished:
		t.Fatalf("write finished without asking: %v", err)
	case <-ctx.Done():
		t.Fatal("write never requested approval")
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("denied exchange did not finish")
	}
	data, err := os.ReadFile("mode-write.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatal("denied write changed file")
	}
}

func TestModeSwitchRebuildsProviderToolsAndKeepsHistory(t *testing.T) {
	p := &stubProvider{name: "stub"}
	m := newTestModelApprovals(t, p, perm.Chat, NewApprovals())
	m.sess.History().Append(provider.RoleUser, "keep this context")
	for _, tt := range []struct {
		mode  string
		tools string
	}{
		{"code", "read_file search_files write_file"},
		{"agent", "read_file search_files write_file run_command"},
		{"chat", ""},
	} {
		m = typing(m, "/mode "+tt.mode)
		m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		if m.mode.String() != tt.mode {
			t.Fatalf("mode = %s, want %s", m.mode, tt.mode)
		}
		if m.sess.History().Turns()[0].Content != "keep this context" {
			t.Fatal("switch lost history")
		}
		drainExchange(t, &m, "what can you do?")
		var names []string
		for _, tool := range p.last.Tools {
			names = append(names, tool.Name)
		}
		if got := strings.Join(names, " "); got != tt.tools {
			t.Errorf("%s tools = %q, want %q", tt.mode, got, tt.tools)
		}
	}
}

func TestModeWithoutApprovalBridgeWithholdsWrites(t *testing.T) {
	p := &stubProvider{name: "stub"}
	m := newTestModel(t, p)
	m = typing(m, "/mode agent")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	drainExchange(t, &m, "hello")
	var names []string
	for _, tool := range p.last.Tools {
		names = append(names, tool.Name)
	}
	if got := strings.Join(names, " "); got != "read_file search_files" {
		t.Fatalf("tools = %q", got)
	}
}

func TestModeInvalidAndQueryLeaveStateAlone(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	for _, text := range []string{"/mode", "/mode root", "/mode agent extra"} {
		m = typing(m, text)
		m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		if m.mode != perm.Chat || m.sess.History().Len() != 0 {
			t.Fatalf("%s changed session", text)
		}
	}
	if !strings.Contains(transcript(m), "mode: chat") {
		t.Fatal(transcript(m))
	}
}

func TestModeWhileBusyKeepsInput(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m.busy = true
	m = typing(m, "/mode agent")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != perm.Chat || m.input.Value() != "/mode agent" {
		t.Fatal("mode changed while busy")
	}
}

func TestModeWaitsForCancelledWorker(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	done := make(chan struct{})
	m.current.done = done
	m = typing(m, "/mode code")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != perm.Chat {
		t.Fatal("changed tools while worker was running")
	}
	close(done)
	m = typing(m, "/mode code")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != perm.Code {
		t.Fatal("mode did not change after worker stopped")
	}
}

func TestModeSwitchChangesLocalReadPermissions(t *testing.T) {
	workspaceDir(t, map[string]string{"example.txt": "workspace content"})
	m := newTestModel(t, &stubProvider{name: "stub"})
	for _, text := range []string{"/mode code", "/read example.txt"} {
		m, _ = command(t, m, text)
	}
	if !strings.Contains(m.blocks[len(m.blocks)-1].text, "workspace content") {
		t.Fatal("code mode could not read file")
	}
	for _, text := range []string{"/mode chat", "/read example.txt"} {
		m, _ = command(t, m, text)
	}
	if !strings.Contains(m.blocks[len(m.blocks)-1].text, "not permitted") {
		t.Fatal("chat downgrade retained file access")
	}
}

func TestModeCannotTakeKeyboardFromApproval(t *testing.T) {
	m := newTestModelApprovals(t, &stubProvider{name: "stub"}, perm.Code, NewApprovals())
	m, _, _ = asking(t, m, pendingApproval(perm.Write, "file.txt", "new content"))
	m = typing(m, "/mode agent")
	m, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != perm.Code {
		t.Fatal("pending approval allowed a mode change")
	}
	if cmd == nil {
		t.Fatal("approval did not consume Enter")
	}
	answer, ok := cmd().(approvalAnswerMsg)
	if !ok || answer.allowed {
		t.Fatal("Enter did not deny pending action")
	}
}
