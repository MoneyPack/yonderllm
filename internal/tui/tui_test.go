// These tests exercise the terminal session without a terminal. Bubble Tea's
// model is a pure function of messages, so almost everything here is done by
// handing a model a message and reading the model that comes back; only the
// end-to-end case runs a command, and it drains the stream by hand rather than
// starting a program. The session underneath is real — only the provider is a
// stub — so the wiring between the two is covered rather than mocked away.
package tui

import (
	"context"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"yonderllm/internal/config"
	"yonderllm/internal/perm"
	"yonderllm/internal/provider"
	"yonderllm/internal/session"
	"yonderllm/internal/tools"
	"yonderllm/internal/workspace"
)

// stubProvider is a provider that replays a fixed list of deltas. It records
// the last request it was given so a test can check what the session actually
// sent, which is the only part of the exchange the model does not see.
type stubProvider struct {
	name   string
	deltas []string
	err    error
	last   provider.Request
}

func (p *stubProvider) Name() string { return p.name }

func (p *stubProvider) Models(ctx context.Context) ([]provider.Model, error) {
	return []provider.Model{{
		ID:            "stub-1",
		Name:          "Stub One",
		ContextWindow: 8192,
		Pricing:       provider.Pricing{Known: true},
	}}, nil
}

func (p *stubProvider) Stream(ctx context.Context, req provider.Request) iter.Seq2[provider.Chunk, error] {
	p.last = req
	return func(yield func(provider.Chunk, error) bool) {
		if p.err != nil {
			yield(provider.Chunk{}, p.err)
			return
		}
		for _, d := range p.deltas {
			if !yield(provider.Chunk{Delta: d}, nil) {
				return
			}
		}
		yield(provider.Chunk{Finish: provider.FinishStop, Usage: &provider.Usage{PromptTokens: 5, CompletionTokens: 7}}, nil)
	}
}

// testConfig builds a two-provider configuration. APIKeyEnv is deliberately
// left empty on both: a provider with no key environment variable counts as
// credentialed, which is what lets these tests reach a stream without touching
// the real environment.
func testConfig() config.Config {
	return config.Config{
		Provider:  "stub",
		Fallbacks: []string{"other"},
		Mode:      "chat",
		MaxTokens: 256,
		DailyCap:  0,
		Providers: map[string]config.ProviderConfig{
			"stub":  {BaseURL: "https://stub.invalid/v1", Model: "stub-1"},
			"other": {BaseURL: "https://other.invalid/v1", Model: "other-1"},
		},
	}
}

// newTestModel returns a model over a real session backed by p, already sized
// so that ready is set and the viewport has a width to reflow against. The mode
// is chat, which is the most restrictive: it denies every file action.
func newTestModel(t *testing.T, p *stubProvider) model {
	t.Helper()
	return newTestModelMode(t, p, perm.Chat)
}

// newTestModelMode is newTestModel with the permission mode chosen by the
// caller, for the commands that only work outside chat mode. The approvals
// bridge is nil, which is what a model gets when no interface can be asked.
func newTestModelMode(t *testing.T, p *stubProvider, mode perm.Mode) model {
	t.Helper()
	return newTestModelApprovals(t, p, mode, nil)
}

// newTestModelApprovals is newTestModelMode with an approvals bridge attached,
// for the tests that drive a question through the message loop. It is a
// separate helper rather than another parameter on newTestModel so that the
// dozens of tests with nothing to approve stay unchanged.
func newTestModelApprovals(t *testing.T, p *stubProvider, mode perm.Mode, approvals *Approvals) model {
	t.Helper()

	sess := session.New(testConfig(), func(name string) (provider.Provider, error) {
		return p, nil
	})

	m := newModel(sess, mode, approvals)
	m.resize(60, 24)
	return m
}

// workspaceDir makes a directory the working directory for the rest of the
// test, so that workspace.Current opens somewhere known instead of the repo.
// Files are given as a path relative to the directory mapped to its contents;
// parent directories are created as needed.
func workspaceDir(t *testing.T, files map[string]string) {
	t.Helper()

	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}
	}
	t.Chdir(dir)
}

// step sends one message and returns the model that results, saving every test
// the type assertion back from tea.Model.
func step(t *testing.T, m model, msg tea.Msg) (model, tea.Cmd) {
	t.Helper()

	next, cmd := m.Update(msg)
	got, ok := next.(model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.model", next)
	}
	return got, cmd
}

// transcript joins the block texts so a test can assert on what was said
// without caring how it was styled or wrapped.
func transcript(m model) string {
	parts := make([]string, 0, len(m.blocks))
	for _, b := range m.blocks {
		parts = append(parts, b.text)
	}
	return strings.Join(parts, "\n")
}

// typing puts text in the input as though it had been typed.
func typing(m model, text string) model {
	m.input.SetValue(text)
	return m
}

func TestNewModelGreets(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})

	if len(m.blocks) != 1 {
		t.Fatalf("got %d starting blocks, want 1", len(m.blocks))
	}
	if m.blocks[0].kind != blockInfo {
		t.Errorf("starting block kind = %v, want blockInfo", m.blocks[0].kind)
	}

	for _, want := range []string{"stub", "stub-1", "chat", "Inference runs remotely", "/help"} {
		if !strings.Contains(m.blocks[0].text, want) {
			t.Errorf("greeting missing %q:\n%s", want, m.blocks[0].text)
		}
	}
}

func TestResizeClampsViewport(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})

	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	if !m.ready {
		t.Fatal("model not ready after a size message")
	}
	if got, want := m.view.Height, 40-headerHeight-footerHeight-inputHeight; got != want {
		t.Errorf("viewport height = %d, want %d", got, want)
	}
	if m.view.Width != 100 {
		t.Errorf("viewport width = %d, want 100", m.view.Width)
	}

	// A terminal too short for the full layout still gets a usable
	// transcript rather than a negative one.
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 40, Height: 4})
	if m.view.Height != minViewport {
		t.Errorf("viewport height = %d on a 4-row terminal, want the %d clamp", m.view.Height, minViewport)
	}
}

func TestViewBeforeSizeShowsBrandOnly(t *testing.T) {
	sess := session.New(testConfig(), func(string) (provider.Provider, error) {
		return &stubProvider{name: "stub"}, nil
	})
	m := newModel(sess, perm.Chat, nil)

	out := m.View()
	if !strings.Contains(out, brandName) {
		t.Errorf("first frame missing the brand:\n%s", out)
	}
	if strings.Contains(out, "enter send") {
		t.Errorf("first frame drew the footer before a size was known:\n%s", out)
	}
}

func TestViewShowsBrandProviderAndFooter(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})

	out := m.View()
	for _, want := range []string{brandName, "stub", "stub-1", "enter send"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}

	m.busy = true
	if out := m.View(); !strings.Contains(out, "ctrl+c stop") {
		t.Errorf("busy footer missing the stop hint:\n%s", out)
	}

	// A pending question owns every key, ctrl+c included, so the footer has
	// to stop offering keys that no longer do what it says.
	m.asking = true
	out = m.View()
	if !strings.Contains(out, "y allow") {
		t.Errorf("asking footer missing the allow hint:\n%s", out)
	}
	if strings.Contains(out, "ctrl+c stop") {
		t.Errorf("asking footer still offers a key the question has taken:\n%s", out)
	}
}

func TestHeaderKeepsModeVisibleWhenMetadataDoesNotFit(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m.width = 20

	out := m.header()
	if !strings.Contains(out, "mode") || !strings.Contains(out, "chat") {
		t.Errorf("narrow header lost permission mode:\n%s", out)
	}
	if strings.Contains(out, "provider") {
		t.Errorf("narrow header kept metadata that cannot fit:\n%s", out)
	}
}

func TestCtrlCQuitsWhenIdleAndCancelsWhenBusy(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})

	_, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c while idle returned no command, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("ctrl+c while idle produced %T, want tea.QuitMsg", cmd())
	}

	// While a reply is in flight the same key stops the reply instead.
	m = typing(m, "hello")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.busy {
		t.Fatal("model not busy after submitting a prompt")
	}

	m, cmd = step(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Errorf("ctrl+c while busy returned %T, want no command", cmd())
	}
	if m.busy {
		t.Error("still busy after cancelling")
	}
	if !strings.Contains(transcript(m), "cancelled") {
		t.Errorf("cancel left no notice:\n%s", transcript(m))
	}
}

func TestCtrlJInsertsNewline(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m = typing(m, "first")

	before := m.input.LineCount()
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})

	if got := m.input.LineCount(); got <= before {
		t.Errorf("line count = %d after ctrl+j, want more than %d", got, before)
	}
	if m.busy {
		t.Error("ctrl+j submitted the prompt")
	}
}

func TestEnterOnEmptyInputDoesNothing(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m = typing(m, "   \n  ")

	before := len(m.blocks)
	m, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if cmd != nil {
		t.Error("blank input produced a command")
	}
	if len(m.blocks) != before {
		t.Errorf("blank input appended %d blocks", len(m.blocks)-before)
	}
	if m.busy {
		t.Error("blank input started a stream")
	}
}

func TestEnterWhileBusyKeepsTheText(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m.busy = true
	m = typing(m, "second question")

	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if got := m.input.Value(); got != "second question" {
		t.Errorf("input = %q after a refused send, want it kept", got)
	}
	if !strings.Contains(transcript(m), "still answering") {
		t.Errorf("no notice explaining the refusal:\n%s", transcript(m))
	}
}

func TestEnterSubmitsAndClearsTheInput(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m = typing(m, "why is the sky blue")

	m, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("submitting returned no command")
	}
	if m.input.Value() != "" {
		t.Errorf("input = %q after submitting, want empty", m.input.Value())
	}
	if !m.busy {
		t.Error("not busy after submitting")
	}
	if m.seq != 1 {
		t.Errorf("seq = %d after one submit, want 1", m.seq)
	}
	if !strings.Contains(transcript(m), "why is the sky blue") {
		t.Errorf("prompt missing from the transcript:\n%s", transcript(m))
	}

	m.cancel()
}

func TestStreamEventsAppendAndCommit(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m.seq = 1
	m.busy = true
	m.answered = "stub"
	m.current = stream{seq: 1, ch: make(chan streamPacket, 1), cancel: func() {}}

	m, cmd := step(t, m, streamEventMsg{seq: 1, packet: streamPacket{event: session.Event{Delta: "blue ", Provider: "stub"}}})
	if cmd == nil {
		t.Fatal("a delta did not ask for the next packet")
	}
	m, _ = step(t, m, streamEventMsg{seq: 1, packet: streamPacket{event: session.Event{Delta: "light", Provider: "stub"}}})

	if m.pending != "blue light" {
		t.Errorf("pending = %q, want %q", m.pending, "blue light")
	}
	// A reply in flight is drawn but not yet part of the transcript.
	if strings.Contains(transcript(m), "blue light") {
		t.Error("a partial reply was committed to the transcript")
	}
	if !strings.Contains(m.view.View(), "blue") {
		t.Errorf("streaming text is not being drawn:\n%s", m.view.View())
	}

	m, _ = step(t, m, streamClosedMsg{seq: 1})
	if m.busy {
		t.Error("still busy after the stream closed")
	}
	if m.pending != "" {
		t.Errorf("pending = %q after finishing, want empty", m.pending)
	}

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockAssistant {
		t.Errorf("last block kind = %v, want blockAssistant", last.kind)
	}
	if last.tag != "stub" {
		t.Errorf("last block tag = %q, want %q", last.tag, "stub")
	}
	if last.text != "blue light" {
		t.Errorf("last block text = %q, want %q", last.text, "blue light")
	}
}

func TestStaleStreamMessagesAreDropped(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m.seq = 2
	m.busy = true
	m.current = stream{seq: 2, ch: make(chan streamPacket, 1), cancel: func() {}}

	before := len(m.blocks)

	// A packet from the previous exchange must not join this one, and must
	// not re-arm the reader either.
	m, cmd := step(t, m, streamEventMsg{seq: 1, packet: streamPacket{event: session.Event{Delta: "stale"}}})
	if cmd != nil {
		t.Error("a stale packet re-issued the read command")
	}
	if m.pending != "" || len(m.blocks) != before {
		t.Errorf("a stale packet changed the transcript: pending=%q blocks=%d", m.pending, len(m.blocks))
	}

	// The same goes for the close of a superseded stream.
	m, _ = step(t, m, streamClosedMsg{seq: 1})
	if !m.busy {
		t.Error("a stale close ended the exchange in flight")
	}
}

func TestStreamErrorAndNoticeStayInTheStream(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m.seq = 1
	m.busy = true
	m.current = stream{seq: 1, ch: make(chan streamPacket, 1), cancel: func() {}}

	m, _ = step(t, m, streamEventMsg{seq: 1, packet: streamPacket{err: context.DeadlineExceeded}})
	if !m.busy {
		t.Error("an error ended the exchange; fallback would have nowhere to report to")
	}
	if last := m.blocks[len(m.blocks)-1]; last.kind != blockError {
		t.Errorf("error block kind = %v, want blockError", last.kind)
	}

	m, _ = step(t, m, streamEventMsg{seq: 1, packet: streamPacket{event: session.Event{Notice: "falling back to other", Provider: "other"}}})
	if last := m.blocks[len(m.blocks)-1]; last.kind != blockNotice || !strings.Contains(last.text, "falling back to other") {
		t.Errorf("notice block = %+v, want a blockNotice carrying the text", last)
	}
	if m.answered != "other" {
		t.Errorf("answered = %q after a fallback notice, want %q", m.answered, "other")
	}
}

func TestHelpCommand(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m = typing(m, "/help")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	text := transcript(m)
	for _, want := range []string{"/model", "/clear", "/read", "/search", "/usage", "/help", "ctrl+j", "pgup / pgdn"} {
		if !strings.Contains(text, want) {
			t.Errorf("help missing %q", want)
		}
	}
	if m.busy {
		t.Error("/help started a stream")
	}
}

func TestUnknownCommandIsReported(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m = typing(m, "/nope please")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError {
		t.Errorf("unknown command block kind = %v, want blockError", last.kind)
	}
	if !strings.Contains(last.text, `unknown command "/nope"`) || !strings.Contains(last.text, "/help") {
		t.Errorf("unknown command text = %q", last.text)
	}
	if m.busy {
		t.Error("an unknown command was sent to a model")
	}
}

func TestClearCommandResetsBothHalves(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m.append(block{kind: blockUser, text: "earlier question"})

	m = typing(m, "/clear")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(m.blocks) != 2 {
		t.Fatalf("got %d blocks after /clear, want 2", len(m.blocks))
	}
	if strings.Contains(transcript(m), "earlier question") {
		t.Errorf("the transcript survived /clear:\n%s", transcript(m))
	}
	if !strings.Contains(transcript(m), "conversation cleared") {
		t.Errorf("no notice that the conversation was cleared:\n%s", transcript(m))
	}
	if n := len(m.sess.History().Turns()); n != 0 {
		t.Errorf("history has %d turns after /clear, want 0", n)
	}
}

func TestReadCommandShowsAFile(t *testing.T) {
	workspaceDir(t, map[string]string{"notes.txt": "alpha\nbeta\n"})

	m := newTestModelMode(t, &stubProvider{name: "stub"}, perm.Code)
	m = typing(m, "/read notes.txt")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockInfo {
		t.Errorf("/read block kind = %v, want blockInfo", last.kind)
	}
	for _, want := range []string{"notes.txt", "alpha", "beta"} {
		if !strings.Contains(last.text, want) {
			t.Errorf("/read output missing %q:\n%s", want, last.text)
		}
	}
	if m.busy {
		t.Error("/read was sent to a model")
	}
}

func TestReadCommandReadsThroughASubdirectory(t *testing.T) {
	workspaceDir(t, map[string]string{"docs/guide.md": "# heading\n"})

	m := newTestModelMode(t, &stubProvider{name: "stub"}, perm.Code)
	m = typing(m, "/read docs/guide.md")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockInfo {
		t.Fatalf("/read block kind = %v, want blockInfo: %s", last.kind, last.text)
	}
	if !strings.Contains(last.text, "# heading") {
		t.Errorf("/read output missing the file contents:\n%s", last.text)
	}
}

// The argument is the raw remainder of the line rather than a parsed field, so
// a name with a space in it survives.
func TestReadCommandKeepsSpacesInTheName(t *testing.T) {
	workspaceDir(t, map[string]string{"two words.txt": "spaced\n"})

	m := newTestModelMode(t, &stubProvider{name: "stub"}, perm.Code)
	m = typing(m, "/read two words.txt")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockInfo {
		t.Fatalf("/read block kind = %v, want blockInfo: %s", last.kind, last.text)
	}
	if !strings.Contains(last.text, "spaced") {
		t.Errorf("/read output missing the file contents:\n%s", last.text)
	}
}

func TestReadCommandWithoutAnArgumentShowsUsage(t *testing.T) {
	m := newTestModelMode(t, &stubProvider{name: "stub"}, perm.Code)
	m = typing(m, "/read")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError {
		t.Errorf("/read block kind = %v, want blockError", last.kind)
	}
	if last.text != "usage: /read <file>" {
		t.Errorf("/read usage text = %q", last.text)
	}
}

// Chat mode denies every file action, and the denial has to reach the
// transcript rather than being silently dropped.
func TestReadCommandIsDeniedInChatMode(t *testing.T) {
	workspaceDir(t, map[string]string{"notes.txt": "alpha\n"})

	m := newTestModel(t, &stubProvider{name: "stub"})
	m = typing(m, "/read notes.txt")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError {
		t.Errorf("denied /read block kind = %v, want blockError", last.kind)
	}
	if last.text != "read is not permitted in chat mode" {
		t.Errorf("denied /read text = %q", last.text)
	}
	if strings.Contains(last.text, "alpha") {
		t.Error("a denied /read leaked the file contents")
	}
}

func TestReadCommandReportsAMissingFile(t *testing.T) {
	workspaceDir(t, nil)

	m := newTestModelMode(t, &stubProvider{name: "stub"}, perm.Code)
	m = typing(m, "/read absent.txt")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError {
		t.Errorf("missing file block kind = %v, want blockError", last.kind)
	}
	if !strings.Contains(last.text, "workspace: read absent.txt") {
		t.Errorf("missing file text = %q", last.text)
	}
}

// A path climbing out of the workspace is refused by name, before any read is
// attempted, so the transcript says the path is outside the workspace rather
// than reporting a failed read.
func TestReadCommandRejectsAPathOutsideTheWorkspace(t *testing.T) {
	workspaceDir(t, map[string]string{"notes.txt": "alpha\n"})

	m := newTestModelMode(t, &stubProvider{name: "stub"}, perm.Code)
	m = typing(m, "/read ../escape.txt")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError {
		t.Errorf("escaping /read block kind = %v, want blockError", last.kind)
	}
	if !strings.Contains(last.text, "../escape.txt is outside the workspace") {
		t.Errorf("escaping /read text = %q", last.text)
	}
}

func TestFileViewLabelsAnEmptyFile(t *testing.T) {
	got := fileView("blank.txt", "")
	if !strings.Contains(got, "blank.txt") || !strings.Contains(got, "(empty file)") {
		t.Errorf("fileView of an empty file = %q", got)
	}
}

// A file of only newlines is empty once the trailing ones are trimmed, so it
// takes the same branch as a zero-byte file.
func TestFileViewTreatsTrailingNewlinesAsEmpty(t *testing.T) {
	if got := fileView("blank.txt", "\n\n\n"); !strings.Contains(got, "(empty file)") {
		t.Errorf("fileView of only newlines = %q", got)
	}
}

func TestFileViewTruncatesALongFile(t *testing.T) {
	lines := make([]string, 0, maxShownLines+10)
	for i := range maxShownLines + 10 {
		lines = append(lines, fmt.Sprintf("line %d", i+1))
	}
	got := fileView("long.txt", strings.Join(lines, "\n"))

	if !strings.Contains(got, "line 1\n") {
		t.Error("fileView dropped the first line")
	}
	if !strings.Contains(got, fmt.Sprintf("line %d", maxShownLines)) {
		t.Errorf("fileView dropped line %d", maxShownLines)
	}
	if strings.Contains(got, fmt.Sprintf("line %d", maxShownLines+1)) {
		t.Errorf("fileView kept line %d, past the cap", maxShownLines+1)
	}
	if !strings.Contains(got, "... 10 more lines") {
		t.Errorf("fileView did not count the lines it withheld:\n%s", got)
	}
}

func TestSearchCommandFindsMatches(t *testing.T) {
	workspaceDir(t, map[string]string{
		"notes.txt":    "alpha\nbeta needle\n",
		"docs/more.md": "needle again\n",
	})

	m := newTestModelMode(t, &stubProvider{name: "stub"}, perm.Code)
	m = typing(m, "/search needle")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockInfo {
		t.Fatalf("/search block kind = %v, want blockInfo: %s", last.kind, last.text)
	}
	for _, want := range []string{`2 matches for "needle"`, "notes.txt:2: beta needle", "docs/more.md:1: needle again"} {
		if !strings.Contains(last.text, want) {
			t.Errorf("/search output missing %q:\n%s", want, last.text)
		}
	}
	if m.busy {
		t.Error("/search was sent to a model")
	}
}

// Search is literal but case-insensitive, so the query need not match the case
// of the text it finds.
func TestSearchCommandIgnoresCase(t *testing.T) {
	workspaceDir(t, map[string]string{"notes.txt": "Needle here\n"})

	m := newTestModelMode(t, &stubProvider{name: "stub"}, perm.Code)
	m = typing(m, "/search NEEDLE")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockInfo {
		t.Fatalf("/search block kind = %v, want blockInfo: %s", last.kind, last.text)
	}
	if !strings.Contains(last.text, "notes.txt:1: Needle here") {
		t.Errorf("/search output missing the match:\n%s", last.text)
	}
}

// The query is the raw remainder of the line, so a phrase keeps its spacing
// instead of collapsing into one word.
func TestSearchCommandKeepsSpacesInTheQuery(t *testing.T) {
	workspaceDir(t, map[string]string{"notes.txt": "two words here\n"})

	m := newTestModelMode(t, &stubProvider{name: "stub"}, perm.Code)
	m = typing(m, "/search two words")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockInfo {
		t.Fatalf("/search block kind = %v, want blockInfo: %s", last.kind, last.text)
	}
	if !strings.Contains(last.text, `1 matches for "two words"`) {
		t.Errorf("/search output missing the phrase query:\n%s", last.text)
	}
}

func TestSearchCommandWithoutAnArgumentShowsUsage(t *testing.T) {
	m := newTestModelMode(t, &stubProvider{name: "stub"}, perm.Code)
	m = typing(m, "/search")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError {
		t.Errorf("/search block kind = %v, want blockError", last.kind)
	}
	if last.text != "usage: /search <text>" {
		t.Errorf("/search usage text = %q", last.text)
	}
}

func TestSearchCommandIsDeniedInChatMode(t *testing.T) {
	workspaceDir(t, map[string]string{"notes.txt": "needle\n"})

	m := newTestModel(t, &stubProvider{name: "stub"})
	m = typing(m, "/search needle")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError {
		t.Errorf("denied /search block kind = %v, want blockError", last.kind)
	}
	if last.text != "search is not permitted in chat mode" {
		t.Errorf("denied /search text = %q", last.text)
	}
}

// Finding nothing is an answer rather than a failure, so it is a notice.
func TestSearchCommandReportsNoMatchesAsANotice(t *testing.T) {
	workspaceDir(t, map[string]string{"notes.txt": "alpha\n"})

	m := newTestModelMode(t, &stubProvider{name: "stub"}, perm.Code)
	m = typing(m, "/search needle")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockNotice {
		t.Errorf("empty /search block kind = %v, want blockNotice", last.kind)
	}
	if last.text != `no matches for "needle"` {
		t.Errorf("empty /search text = %q", last.text)
	}
}

func TestMatchViewTruncatesManyMatches(t *testing.T) {
	matches := make([]workspace.Match, 0, maxShownLines+3)
	for i := range maxShownLines + 3 {
		matches = append(matches, workspace.Match{
			Path: "notes.txt",
			Line: i + 1,
			Text: fmt.Sprintf("hit %d", i+1),
		})
	}
	got := matchView("hit", matches)

	if !strings.Contains(got, fmt.Sprintf("%d matches for %q", len(matches), "hit")) {
		t.Errorf("matchView heading did not count every match:\n%s", got)
	}
	if !strings.Contains(got, fmt.Sprintf("notes.txt:%d:", maxShownLines)) {
		t.Errorf("matchView dropped match %d", maxShownLines)
	}
	if strings.Contains(got, fmt.Sprintf("notes.txt:%d:", maxShownLines+1)) {
		t.Errorf("matchView kept match %d, past the cap", maxShownLines+1)
	}
	if !strings.Contains(got, "... 3 more matches") {
		t.Errorf("matchView did not count the matches it withheld:\n%s", got)
	}
}

func TestUsageCommand(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m = typing(m, "/usage")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	text := m.blocks[len(m.blocks)-1].text
	for _, want := range []string{"requests  0", "remaining uncapped", "no requests yet this session"} {
		if !strings.Contains(text, want) {
			t.Errorf("usage report missing %q:\n%s", want, text)
		}
	}
}

func TestUsageCommandShowsTheCap(t *testing.T) {
	cfg := testConfig()
	cfg.DailyCap = 20
	sess := session.New(cfg, func(string) (provider.Provider, error) {
		return &stubProvider{name: "stub"}, nil
	})
	m := newModel(sess, perm.Chat, nil)
	m.resize(60, 24)

	m.append(block{kind: blockInfo, text: m.usageReport()})

	text := m.blocks[len(m.blocks)-1].text
	if !strings.Contains(text, "of 20") || !strings.Contains(text, "remaining 20") {
		t.Errorf("capped usage report = %q", text)
	}
}

func TestModelCommandReportsWithNoArguments(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m = typing(m, "/model")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	text := m.blocks[len(m.blocks)-1].text
	for _, want := range []string{"provider  stub", "model     stub-1", "mode      chat", "configured providers", "* stub", "other"} {
		if !strings.Contains(text, want) {
			t.Errorf("model report missing %q:\n%s", want, text)
		}
	}
}

func TestModelCommandSwitchesProvider(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m = typing(m, "/model other")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if got := m.sess.Provider(); got != "other" {
		t.Errorf("provider = %q, want %q", got, "other")
	}
	if got := m.sess.Model(); got != "other-1" {
		t.Errorf("model = %q, want the provider's configured %q", got, "other-1")
	}
	if !strings.Contains(transcript(m), "now using other (other-1)") {
		t.Errorf("no notice of the switch:\n%s", transcript(m))
	}
}

func TestModelCommandSwitchesModelOnTheCurrentProvider(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m = typing(m, "/model stub-9")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if got := m.sess.Provider(); got != "stub" {
		t.Errorf("provider = %q, want it unchanged at %q", got, "stub")
	}
	if got := m.sess.Model(); got != "stub-9" {
		t.Errorf("model = %q, want %q", got, "stub-9")
	}
	if !strings.Contains(transcript(m), "model is now stub-9 on stub") {
		t.Errorf("no notice of the model change:\n%s", transcript(m))
	}
}

func TestModelCommandSwitchesBothAtOnce(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m = typing(m, "/model other other-9")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if got, want := m.sess.Provider(), "other"; got != want {
		t.Errorf("provider = %q, want %q", got, want)
	}
	if got, want := m.sess.Model(), "other-9"; got != want {
		t.Errorf("model = %q, want %q", got, want)
	}
}

func TestModelCommandRejectsAnUnknownProvider(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m = typing(m, "/model nowhere elsewhere")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if got := m.sess.Provider(); got != "stub" {
		t.Errorf("provider = %q, want the failed switch to have left %q", got, "stub")
	}
	if got := m.sess.Model(); got != "stub-1" {
		t.Errorf("model = %q; a rejected switch must not apply half of itself", got)
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError || !strings.Contains(last.text, "nowhere") {
		t.Errorf("last block = %+v, want an error naming the provider", last)
	}
}

func TestModelCommandRejectsTooManyArguments(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m = typing(m, "/model a b c")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError || !strings.Contains(last.text, "usage: /model") {
		t.Errorf("last block = %+v, want the usage line", last)
	}
}

func TestBlockRender(t *testing.T) {
	s := newStyles()

	cases := []struct {
		name  string
		block block
		want  []string
	}{
		{"user", block{kind: blockUser, text: "question"}, []string{"you", "question"}},
		{"assistant tagged", block{kind: blockAssistant, tag: "groq", text: "answer"}, []string{"groq", "answer"}},
		{"assistant untagged", block{kind: blockAssistant, text: "answer"}, []string{"assistant", "answer"}},
		{"notice", block{kind: blockNotice, text: "heads up"}, []string{"·", "heads up"}},
		{"error", block{kind: blockError, text: "it broke"}, []string{"error", "it broke"}},
		{"info", block{kind: blockInfo, text: "reference"}, []string{"reference"}},
		{"tool named", block{kind: blockTool, tag: "read_file", text: "main.go"}, []string{"tool read_file", "main.go"}},
		{"tool unnamed", block{kind: blockTool, text: "something"}, []string{"tool tool", "something"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.block.render(s, 40)
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("render missing %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestBlockRenderClampsNarrowWidths(t *testing.T) {
	s := newStyles()

	// A width of zero or less would make the wrap arithmetic meaningless,
	// so it is floored rather than trusted.
	out := block{kind: blockInfo, text: "wrap me somewhere sensible"}.render(s, 0)
	if strings.TrimSpace(out) == "" {
		t.Fatal("rendering at width 0 produced nothing")
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 40 {
			t.Errorf("line %q is wider than the clamp allows", line)
		}
	}
}

func TestForwardReachesTheChildComponents(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})

	// A rune key is not one the session claims, so it must land in the
	// textarea.
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	if got := m.input.Value(); got != "h" {
		t.Errorf("input = %q after typing, want %q", got, "h")
	}
}

func TestEndToEndExchange(t *testing.T) {
	p := &stubProvider{name: "stub", deltas: []string{"the sky ", "is blue"}}
	m := newTestModel(t, p)

	m = typing(m, "why is the sky blue")
	m, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("submitting returned no command to drain")
	}

	// Bubble Tea would run each command and feed the result back; here that
	// loop is run by hand until the stream reports itself closed.
	for i := 0; cmd != nil && i < 64; i++ {
		msg := cmd()
		m, cmd = step(t, m, msg)
		if _, closed := msg.(streamClosedMsg); closed {
			break
		}
	}

	if m.busy {
		t.Fatal("still busy after the exchange finished")
	}

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockAssistant {
		t.Fatalf("last block kind = %v, want blockAssistant", last.kind)
	}
	if last.text != "the sky is blue" {
		t.Errorf("reply = %q, want %q", last.text, "the sky is blue")
	}
	if last.tag != "stub" {
		t.Errorf("reply tag = %q, want %q", last.tag, "stub")
	}

	// The prompt reached the provider, and the exchange was counted.
	if p.last.Model != "stub-1" {
		t.Errorf("provider was asked for model %q, want %q", p.last.Model, "stub-1")
	}
	if n := len(p.last.Messages); n == 0 {
		t.Fatal("provider was sent no messages")
	}
	if got := p.last.Messages[len(p.last.Messages)-1].Content; got != "why is the sky blue" {
		t.Errorf("last message = %q, want the prompt", got)
	}
	if got := m.sess.Usage().Requests(); got != 1 {
		t.Errorf("usage recorded %d requests, want 1", got)
	}
}

// toolBlocks returns the tool entries in the transcript, so that a test can
// assert a call was rewritten rather than reported twice.
func toolBlocks(m model) []block {
	var found []block
	for _, b := range m.blocks {
		if b.kind == blockTool {
			found = append(found, b)
		}
	}
	return found
}

// streaming puts a model into the middle of an exchange, which is the only
// state in which stream messages are accepted.
func streaming(m model, provider string) model {
	m.seq = 1
	m.busy = true
	m.answered = provider
	m.current = stream{seq: 1, ch: make(chan streamPacket, 1), cancel: func() {}}
	return m
}

func TestToolResultReplacesTheStartedCall(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m = streaming(m, "stub")

	// A tool call is announced before it runs so that a slow search does
	// not look like a stalled session.
	start := &session.ToolRun{ID: "call-1", Name: "read_file", Arguments: `{"path": "main.go"}`}
	m, cmd := step(t, m, streamEventMsg{seq: 1, packet: streamPacket{event: session.Event{Provider: "stub", Tool: start}}})
	if cmd == nil {
		t.Fatal("a tool event did not ask for the next packet")
	}

	shown := toolBlocks(m)
	if len(shown) != 1 {
		t.Fatalf("tool blocks after the start = %d, want 1", len(shown))
	}
	if shown[0].tag != "read_file" {
		t.Errorf("tool block tag = %q, want %q", shown[0].tag, "read_file")
	}
	if shown[0].id != "call-1" {
		t.Errorf("tool block id = %q, want %q", shown[0].id, "call-1")
	}
	if !strings.Contains(shown[0].text, "main.go") {
		t.Errorf("tool block does not show its arguments:\n%s", shown[0].text)
	}
	if !strings.Contains(shown[0].text, "(no output)") {
		t.Errorf("a call with no result yet should say so:\n%s", shown[0].text)
	}

	// The result arrives as a second event carrying the same id, and takes
	// the place of the announcement instead of following it.
	done := &session.ToolRun{ID: "call-1", Name: "read_file", Arguments: `{"path": "main.go"}`, Finished: true, Result: "package main"}
	m, _ = step(t, m, streamEventMsg{seq: 1, packet: streamPacket{event: session.Event{Provider: "stub", Tool: done}}})

	shown = toolBlocks(m)
	if len(shown) != 1 {
		t.Fatalf("tool blocks after the result = %d, want 1", len(shown))
	}
	if !strings.Contains(shown[0].text, "package main") {
		t.Errorf("tool block does not show its result:\n%s", shown[0].text)
	}
	if strings.Contains(shown[0].text, "(no output)") {
		t.Errorf("the finished call still reads as unanswered:\n%s", shown[0].text)
	}
}

func TestToolErrorIsShownInTheToolBlock(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m = streaming(m, "stub")

	// A tool that fails is still a tool that ran: the failure belongs with
	// the call, not in an error block of its own, because the model is
	// about to be told the same thing and may recover from it.
	failed := &session.ToolRun{ID: "call-1", Name: "read_file", Arguments: `{"path": "nope.go"}`, Finished: true, Err: "workspace: read nope.go: no file named that"}
	m, _ = step(t, m, streamEventMsg{seq: 1, packet: streamPacket{event: session.Event{Provider: "stub", Tool: failed}}})

	shown := toolBlocks(m)
	if len(shown) != 1 {
		t.Fatalf("tool blocks = %d, want 1", len(shown))
	}
	if !strings.Contains(shown[0].text, "error: ") {
		t.Errorf("a failed call is not labelled as one:\n%s", shown[0].text)
	}
	if !strings.Contains(shown[0].text, "no file named that") {
		t.Errorf("the failure text was dropped:\n%s", shown[0].text)
	}
}

func TestTextBeforeAToolIsCommittedFirst(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m = streaming(m, "stub")

	// A model often says what it is about to do before doing it. That text
	// has to land above the call rather than be held back and end up below
	// it once the exchange finishes.
	m, _ = step(t, m, streamEventMsg{seq: 1, packet: streamPacket{event: session.Event{Delta: "let me look", Provider: "stub"}}})
	run := &session.ToolRun{ID: "call-1", Name: "read_file", Arguments: `{"path": "main.go"}`}
	m, _ = step(t, m, streamEventMsg{seq: 1, packet: streamPacket{event: session.Event{Provider: "stub", Tool: run}}})

	if m.pending != "" {
		t.Errorf("pending = %q, want it committed before the call", m.pending)
	}
	if n := len(m.blocks); n < 2 {
		t.Fatalf("blocks = %d, want the text and the call", n)
	}
	said := m.blocks[len(m.blocks)-2]
	if said.kind != blockAssistant || said.text != "let me look" {
		t.Errorf("block above the call = %+v, want the assistant text", said)
	}
	if said.tag != "stub" {
		t.Errorf("committed text tag = %q, want %q", said.tag, "stub")
	}
	if last := m.blocks[len(m.blocks)-1]; last.kind != blockTool {
		t.Errorf("last block kind = %v, want blockTool", last.kind)
	}
}

func TestStaleToolEventsAreDropped(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m.seq = 2
	m.busy = true
	m.current = stream{seq: 2, ch: make(chan streamPacket, 1), cancel: func() {}}

	// A tool call from an exchange the person already cancelled must not
	// appear under the one that replaced it.
	run := &session.ToolRun{ID: "call-1", Name: "read_file", Finished: true, Result: "package main"}
	m, cmd := step(t, m, streamEventMsg{seq: 1, packet: streamPacket{event: session.Event{Provider: "stub", Tool: run}}})
	if cmd != nil {
		t.Error("a stale tool event re-issued the read command")
	}
	if n := len(toolBlocks(m)); n != 0 {
		t.Errorf("tool blocks = %d, want none from a stale stream", n)
	}
}

func TestToolViewPairsArgumentsWithTheResult(t *testing.T) {
	cases := []struct {
		name      string
		arguments string
		result    string
		want      string
	}{
		{
			name:      "result indented under the call",
			arguments: "{}",
			result:    "one\ntwo",
			want:      "{}\n  one\n  two",
		},
		{
			name:      "blank lines are left blank",
			arguments: "{}",
			result:    "one\n\ntwo",
			want:      "{}\n  one\n\n  two",
		},
		{
			name:      "nothing back is said out loud",
			arguments: "{}",
			result:    "",
			want:      "{}\n  (no output)",
		},
		{
			name:      "a trailing newline is not output",
			arguments: "{}",
			result:    "\n\n",
			want:      "{}\n  (no output)",
		},
		{
			name:      "no arguments at all",
			arguments: "",
			result:    "done",
			want:      "(no arguments)\n  done",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolView(tc.arguments, tc.result); got != tc.want {
				t.Errorf("toolView = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestToolArgsCollapsesWhitespaceAndTruncates(t *testing.T) {
	// However the model laid the JSON out, the transcript wants one line.
	if got, want := toolArgs("{\n  \"path\": \"main.go\"\n}"), `{ "path": "main.go" }`; got != want {
		t.Errorf("toolArgs = %q, want %q", got, want)
	}

	long := toolArgs(`{"query": "` + strings.Repeat("x", maxToolArgBytes*2) + `"}`)
	if len(long) != maxToolArgBytes+3 {
		t.Errorf("truncated arguments are %d bytes, want %d", len(long), maxToolArgBytes+3)
	}
	if !strings.HasSuffix(long, "...") {
		t.Errorf("truncated arguments do not say so: %q", long)
	}

	// Multi-byte input must never be cut in the middle of a character.
	wide := toolArgs(strings.Repeat("世", maxToolArgBytes))
	if !utf8.ValidString(wide) {
		t.Errorf("truncation split a rune: %q", wide)
	}
	if len(wide) > maxToolArgBytes+3 {
		t.Errorf("truncated arguments are %d bytes, want at most %d", len(wide), maxToolArgBytes+3)
	}
	if !strings.HasSuffix(wide, "...") {
		t.Errorf("truncated arguments do not say so: %q", wide)
	}
}

func TestClipLinesReportsWhatItDropped(t *testing.T) {
	if got := clipLines("\n\n", maxToolLines); got != "" {
		t.Errorf("clipLines of blank text = %q, want empty", got)
	}
	if got, want := clipLines("one\ntwo\n", maxToolLines), "one\ntwo"; got != want {
		t.Errorf("clipLines = %q, want %q", got, want)
	}

	var lines []string
	for i := range maxToolLines + 10 {
		lines = append(lines, fmt.Sprintf("line %d", i+1))
	}
	out := clipLines(strings.Join(lines, "\n"), maxToolLines)

	if !strings.Contains(out, "line 1\n") {
		t.Errorf("the start of the result was dropped:\n%s", out)
	}
	if !strings.Contains(out, fmt.Sprintf("line %d", maxToolLines)) {
		t.Errorf("the last kept line is missing:\n%s", out)
	}
	if strings.Contains(out, fmt.Sprintf("line %d", maxToolLines+1)) {
		t.Errorf("a line past the limit survived:\n%s", out)
	}
	if !strings.Contains(out, "... 10 more lines") {
		t.Errorf("the dropped count is missing:\n%s", out)
	}
}

// pendingApproval builds a question of the shape Ask sends, with a reply channel
// a test can read the decision back out of.
func pendingApproval(action perm.Action, target, detail string) approvalRequest {
	return approvalRequest{
		action: action,
		target: target,
		detail: detail,
		reply:  make(chan bool, 1),
	}
}

// asking puts a question on screen and hands back both the model and the request
// as the model now holds it, with the id it was given.
func asking(t *testing.T, m model, req approvalRequest) (model, approvalRequest, tea.Cmd) {
	t.Helper()

	m, cmd := step(t, m, approvalRequestMsg{request: req})
	if !m.asking {
		t.Fatal("the model is not asking after a request arrived")
	}
	return m, m.question, cmd
}

func TestApprovalRequestIsDrawnAsAQuestion(t *testing.T) {
	m := newTestModelApprovals(t, &stubProvider{name: "stub"}, perm.Code, NewApprovals())

	// The question is a transcript block like any other, tagged with the
	// action so the user can see at a glance what kind of call this is.
	m, req, cmd := asking(t, m, pendingApproval(perm.Write, "notes.md", "+ hello"))

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockApproval {
		t.Errorf("last block kind = %v, want blockApproval", last.kind)
	}
	if last.tag != "write" {
		t.Errorf("question tag = %q, want %q", last.tag, "write")
	}
	if req.id != "approval-1" {
		t.Errorf("question id = %q, want %q", req.id, "approval-1")
	}
	if last.id != req.id {
		t.Errorf("block id = %q, want %q", last.id, req.id)
	}

	for _, want := range []string{"notes.md", "+ hello", approvalPrompt} {
		if !strings.Contains(last.text, want) {
			t.Errorf("the question is missing %q:\n%s", want, last.text)
		}
	}

	// Handling one question has to arm the listener for the next one, or the
	// second tool call of a session would wait forever.
	if cmd == nil {
		t.Error("a question did not ask for the next one")
	}
}

func TestApprovalDetailIsClippedToAScreen(t *testing.T) {
	m := newTestModelApprovals(t, &stubProvider{name: "stub"}, perm.Code, NewApprovals())

	var lines []string
	for i := range maxApprovalLines + 5 {
		lines = append(lines, fmt.Sprintf("+ line %d", i+1))
	}

	// A diff longer than a screen has stopped helping anyone judge the call,
	// so the prompt says how much it is not showing.
	m, _, _ = asking(t, m, pendingApproval(perm.Write, "big.txt", strings.Join(lines, "\n")))

	text := m.blocks[len(m.blocks)-1].text
	if strings.Contains(text, fmt.Sprintf("+ line %d", maxApprovalLines+1)) {
		t.Errorf("a line past the limit survived:\n%s", text)
	}
	if !strings.Contains(text, "... 5 more lines") {
		t.Errorf("the dropped count is missing:\n%s", text)
	}
}

func TestOnlyABareYAllowsACall(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyMsg
		want bool
	}{
		{"a lowercase y allows", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}, true},
		{"an uppercase Y allows", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Y'}}, true},
		{"n denies", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}, false},
		{"esc denies", tea.KeyMsg{Type: tea.KeyEsc}, false},
		{"ctrl+c denies", tea.KeyMsg{Type: tea.KeyCtrlC}, false},
		{"enter denies", tea.KeyMsg{Type: tea.KeyEnter}, false},
		{"a stray letter denies", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}, false},
		{"alt+y denies", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}, Alt: true}, false},
		{"yes typed as a word denies", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("yes")}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := allowsApproval(tc.key); got != tc.want {
				t.Errorf("allowsApproval(%v) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestAnsweringSendsTheDecisionBackToTheTool(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyMsg
		want bool
	}{
		{"y allows the call", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}, true},
		{"n denies the call", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}, false},
		{"esc denies the call", tea.KeyMsg{Type: tea.KeyEsc}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModelApprovals(t, &stubProvider{name: "stub"}, perm.Code, NewApprovals())
			m, req, _ := asking(t, m, pendingApproval(perm.Exec, "go", `go test ./...`))

			// The decision has to reach the goroutine parked inside
			// Ask, and the message loop has to hear about it so the
			// transcript can be rewritten.
			_, cmd := step(t, m, tc.key)
			if cmd == nil {
				t.Fatal("a keystroke did not answer the question")
			}

			msg, ok := cmd().(approvalAnswerMsg)
			if !ok {
				t.Fatalf("answering produced %T, want tui.approvalAnswerMsg", cmd())
			}
			if msg.allowed != tc.want {
				t.Errorf("answer message allowed = %v, want %v", msg.allowed, tc.want)
			}
			if msg.request.id != req.id {
				t.Errorf("answer names %q, want %q", msg.request.id, req.id)
			}

			select {
			case got := <-req.reply:
				if got != tc.want {
					t.Errorf("the tool was told %v, want %v", got, tc.want)
				}
			default:
				t.Error("the tool was never told anything")
			}
		})
	}
}

func TestTheAnswerReplacesTheQuestionInPlace(t *testing.T) {
	m := newTestModelApprovals(t, &stubProvider{name: "stub"}, perm.Code, NewApprovals())
	m, req, _ := asking(t, m, pendingApproval(perm.Write, "notes.md", "+ hello"))

	at := len(m.blocks) - 1
	// Nothing promises the question is still the last block by the time the
	// answer comes back, so the block is found by id.
	m.append(block{kind: blockInfo, text: "something else happened"})

	before := len(m.blocks)
	m, cmd := step(t, m, approvalAnswerMsg{request: req, allowed: true})

	if cmd != nil {
		t.Errorf("recording an answer asked for more work: %T", cmd())
	}
	if len(m.blocks) != before {
		t.Errorf("blocks = %d, want %d — the answer was appended, not written in", len(m.blocks), before)
	}
	if m.asking {
		t.Error("the model is still asking after the answer arrived")
	}
	if m.question != (approvalRequest{}) {
		t.Errorf("question = %+v, want it cleared", m.question)
	}

	answered := m.blocks[at]
	if answered.kind != blockApproval || answered.id != req.id {
		t.Fatalf("block %d = %+v, want the question rewritten", at, answered)
	}
	if !strings.Contains(answered.text, approvalAllowed) {
		t.Errorf("the decision is missing:\n%s", answered.text)
	}
	if strings.Contains(transcript(m), approvalPrompt) {
		t.Errorf("the prompt outlived the answer:\n%s", transcript(m))
	}
}

func TestDenyingIsRecordedAsSuch(t *testing.T) {
	m := newTestModelApprovals(t, &stubProvider{name: "stub"}, perm.Agent, NewApprovals())
	m, req, _ := asking(t, m, pendingApproval(perm.Exec, "rm", "rm -rf ."))

	m, _ = step(t, m, approvalAnswerMsg{request: req, allowed: false})

	last := m.blocks[len(m.blocks)-1]
	if !strings.Contains(last.text, approvalDenied) {
		t.Errorf("a refusal is not recorded:\n%s", last.text)
	}
}

func TestTheQuestionOwnsTheKeyboardWhileItIsOpen(t *testing.T) {
	m := newTestModelApprovals(t, &stubProvider{name: "stub"}, perm.Code, NewApprovals())
	m = typing(m, "a prompt half written")
	m, _, _ = asking(t, m, pendingApproval(perm.Write, "notes.md", "+ hello"))

	before := len(m.blocks)

	// Enter is an answer, not a submission: the half-written prompt stays
	// where it is and no exchange starts.
	m, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if _, ok := cmd().(approvalAnswerMsg); !ok {
		t.Fatalf("enter produced %T, want tui.approvalAnswerMsg", cmd())
	}
	if got := m.input.Value(); got != "a prompt half written" {
		t.Errorf("input = %q, want the text left alone", got)
	}
	if m.busy {
		t.Error("enter started an exchange while a question was open")
	}
	if len(m.blocks) != before {
		t.Errorf("blocks = %d, want %d — enter added to the transcript", len(m.blocks), before)
	}
}

func TestCtrlCAnswersTheQuestionRatherThanQuitting(t *testing.T) {
	m := newTestModelApprovals(t, &stubProvider{name: "stub"}, perm.Code, NewApprovals())
	m, _, _ = asking(t, m, pendingApproval(perm.Exec, "go", "go build ./..."))

	// Ctrl+c with a question on screen is a refusal of that call. Quitting
	// the session out from under a parked tool is not what was asked for.
	_, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	msg, ok := cmd().(approvalAnswerMsg)
	if !ok {
		t.Fatalf("ctrl+c produced %T, want tui.approvalAnswerMsg", cmd())
	}
	if msg.allowed {
		t.Error("ctrl+c allowed the call")
	}
}

func TestTextBeforeAQuestionIsCommittedFirst(t *testing.T) {
	m := newTestModelApprovals(t, &stubProvider{name: "stub"}, perm.Code, NewApprovals())
	m = streaming(m, "stub")

	// Whatever the model said on its way to the call belongs above the
	// question, not below the decision.
	m, _ = step(t, m, streamEventMsg{seq: 1, packet: streamPacket{event: session.Event{Delta: "I will write that file", Provider: "stub"}}})
	m, _, _ = asking(t, m, pendingApproval(perm.Write, "notes.md", "+ hello"))

	if m.pending != "" {
		t.Errorf("pending = %q, want it committed before the question", m.pending)
	}
	if n := len(m.blocks); n < 2 {
		t.Fatalf("blocks = %d, want the text and the question", n)
	}
	said := m.blocks[len(m.blocks)-2]
	if said.kind != blockAssistant || said.text != "I will write that file" {
		t.Errorf("block above the question = %+v, want the assistant text", said)
	}
}

func TestEachQuestionGetsItsOwnId(t *testing.T) {
	m := newTestModelApprovals(t, &stubProvider{name: "stub"}, perm.Code, NewApprovals())

	first := pendingApproval(perm.Write, "one.md", "+ one")
	m, first, _ = asking(t, m, first)
	m, _ = step(t, m, approvalAnswerMsg{request: first, allowed: true})

	second := pendingApproval(perm.Write, "two.md", "+ two")
	m, second, _ = asking(t, m, second)

	// Ids have to differ, or answering the second question would rewrite
	// the record of the first.
	if second.id == first.id {
		t.Errorf("both questions are %q, want distinct ids", second.id)
	}

	m, _ = step(t, m, approvalAnswerMsg{request: second, allowed: false})

	if got := transcript(m); !strings.Contains(got, approvalAllowed) || !strings.Contains(got, approvalDenied) {
		t.Errorf("both decisions should be on record:\n%s", got)
	}
}

func TestAskCarriesTheRequestAndWaitsForTheAnswer(t *testing.T) {
	a := NewApprovals()
	answered := make(chan bool, 1)
	go func() {
		answered <- a.Ask(context.Background(), tools.Request{Action: perm.Exec, Target: "go", Detail: `go test ./...`})
	}()

	// What the tool describes is what the interface is asked about.
	req := <-a.ch
	if req.action != perm.Exec || req.target != "go" || req.detail != `go test ./...` {
		t.Errorf("request = %+v, want the tool's own words", req)
	}

	req.reply <- true
	if !<-answered {
		t.Error("Ask returned deny after the user allowed the call")
	}
}

func TestAskDeniesWhenNobodyCanAnswer(t *testing.T) {
	a := NewApprovals()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// The exchange is already over, so there is no interface waiting to read
	// this question. It must not become a yes by default.
	if a.Ask(ctx, tools.Request{Action: perm.Write, Target: "notes.md"}) {
		t.Error("Ask allowed a call nobody was asked about")
	}
}

func TestAskDeniesWhenTheExchangeIsCancelledMidQuestion(t *testing.T) {
	a := NewApprovals()
	ctx, cancel := context.WithCancel(context.Background())

	answered := make(chan bool, 1)
	go func() {
		answered <- a.Ask(ctx, tools.Request{Action: perm.Write, Target: "notes.md"})
	}()

	// The question reached the screen, then the user pressed ctrl+c on the
	// exchange itself. A question nobody can answer any more is a no.
	<-a.ch
	cancel()

	if <-answered {
		t.Error("Ask allowed a call after the exchange was cancelled")
	}
}

func TestAnsweringAnAbandonedQuestionDoesNotBlockTheLoop(t *testing.T) {
	req := pendingApproval(perm.Write, "notes.md", "+ hello")
	req.id = "approval-1"
	req.reply <- false

	// A loop parked in a send is an interface that has stopped redrawing, so
	// an answer with nowhere to go is dropped and only the transcript is
	// updated.
	msg, ok := answerApproval(req, true)().(approvalAnswerMsg)
	if !ok {
		t.Fatal("answering an abandoned question produced no message")
	}
	if !msg.allowed || msg.request.id != req.id {
		t.Errorf("answer message = %+v, want the decision for %q", msg, req.id)
	}
}

func TestWithoutApprovalsNothingIsWaitedOn(t *testing.T) {
	// A session with no approval bridge — a one-shot run, say — must not
	// hand the loop a command that blocks forever.
	if cmd := waitForApproval(nil); cmd != nil {
		t.Error("waitForApproval(nil) returned a command")
	}
}
