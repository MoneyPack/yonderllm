// These tests exercise the terminal session without a terminal. Bubble Tea's
// model is a pure function of messages, so almost everything here is done by
// handing a model a message and reading the model that comes back; only the
// end-to-end case runs a command, and it drains the stream by hand rather than
// starting a program. The session underneath is real — only the provider is a
// stub — so the wiring between the two is covered rather than mocked away.
package tui

import (
	"context"
	"iter"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"yonderllm/internal/config"
	"yonderllm/internal/perm"
	"yonderllm/internal/provider"
	"yonderllm/internal/session"
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
// so that ready is set and the viewport has a width to reflow against.
func newTestModel(t *testing.T, p *stubProvider) model {
	t.Helper()

	sess := session.New(testConfig(), func(name string) (provider.Provider, error) {
		return p, nil
	})

	m := newModel(sess, perm.Chat)
	m.resize(60, 24)
	return m
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

	for _, want := range []string{"stub", "stub-1", "chat", "Inference runs remotely"} {
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
	m := newModel(sess, perm.Chat)

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
	for _, want := range []string{"/model", "/clear", "/usage", "/help", "ctrl+j", "pgup / pgdn"} {
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
	m := newModel(sess, perm.Chat)
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
