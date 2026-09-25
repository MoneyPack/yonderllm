// Tests for the run command.
//
// run is the command a program types. Its plain form must behave exactly like
// ask, because the two share one streaming implementation and a drift between
// them would be invisible until someone piped the wrong one. Its --json form
// is a published interface: one JSON object per line, a type field to switch
// on, and a final done object carrying usage. Failures matter as much as
// successes here, since a consumer that only reads stdout still has to learn
// that something went wrong.
package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunStreamsPlainText(t *testing.T) {
	h := newHarness(t, newStub(t, "quicksort, ", "mergesort, heapsort"))

	r := h.run(t, "run", "list three sorting algorithms")

	wantCode(t, r, 0)
	if got := r.stdout; got != "quicksort, mergesort, heapsort\n" {
		t.Errorf("stdout = %q, want %q", got, "quicksort, mergesort, heapsort\n")
	}
}

func TestRunKeepsPlainStdoutFreeOfJSON(t *testing.T) {
	h := newHarness(t, newStub(t, "just the answer"))

	r := h.run(t, "run", "hello")

	wantCode(t, r, 0)
	// Without --json a caller is reading text; a stray brace would mean the
	// two modes had bled into each other.
	wantNotContains(t, "stdout", r.stdout, "{", "\"type\"", "yonderllm:")
}

func TestRunReadsThePromptFromStdin(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.runWithInput(t, "explain this diff\n", "run")

	wantCode(t, r, 0)
	wantLastUserMessage(t, h, "explain this diff")
}

func TestRunReadsThePromptFromStdinInJSONMode(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.runWithInput(t, "explain this diff\n", "run", "--json")

	wantCode(t, r, 0)
	wantLastUserMessage(t, h, "explain this diff")
}

func TestRunJoinsMultipleArguments(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.run(t, "run", "explain", "the", "borrow", "checker")

	wantCode(t, r, 0)
	wantLastUserMessage(t, h, "explain the borrow checker")
}

func TestRunRejectsAnEmptyPrompt(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.runWithInput(t, "", "run")

	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr, "no prompt")
	if h.stub.seen {
		t.Error("the backend was called despite there being no prompt")
	}
}

func TestRunRejectsAnEmptyPromptInJSONMode(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.runWithInput(t, "   \n\t\n", "run", "--json")

	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr, "no prompt")
	// The failure happened before any event could be produced, so stdout
	// stays empty rather than carrying a half-formed stream.
	if r.stdout != "" {
		t.Errorf("stdout = %q, want empty", r.stdout)
	}
}

func TestRunJSONEmitsOneObjectPerLine(t *testing.T) {
	h := newHarness(t, newStub(t, "alpha", "beta"))

	r := h.run(t, "run", "--json", "hello")

	wantCode(t, r, 0)
	events := decodeNDJSON[wireEvent](t, r.stdout)
	if len(events) == 0 {
		t.Fatalf("no events on stdout\n--- stdout ---\n%s", r.stdout)
	}
	for i, ev := range events {
		if ev.Type == "" {
			t.Errorf("event %d has no type\n--- stdout ---\n%s", i, r.stdout)
		}
	}
}

func TestRunJSONStreamsDeltasInOrder(t *testing.T) {
	h := newHarness(t, newStub(t, "alpha ", "beta ", "gamma"))

	r := h.run(t, "run", "--json", "hello")

	wantCode(t, r, 0)
	if got, want := joinDeltas(t, r.stdout), "alpha beta gamma"; got != want {
		t.Errorf("deltas = %q, want %q", got, want)
	}
}

func TestRunJSONEndsWithADoneEvent(t *testing.T) {
	h := newHarness(t, newStub(t, "alpha"))

	r := h.run(t, "run", "--json", "hello")

	wantCode(t, r, 0)
	events := decodeNDJSON[wireEvent](t, r.stdout)
	last := events[len(events)-1]
	if last.Type != "done" {
		t.Errorf("last event type = %q, want %q", last.Type, "done")
	}
}

func TestRunJSONReportsUsageOnTheDoneEvent(t *testing.T) {
	h := newHarness(t, newStub(t, "alpha"))

	r := h.run(t, "run", "--json", "hello")

	wantCode(t, r, 0)
	done := lastEventOfType(t, r.stdout, "done")
	if done.Usage == nil {
		t.Fatal("the done event carries no usage")
	}
	if got := done.Usage.PromptTokens; got != 11 {
		t.Errorf("prompt_tokens = %d, want %d", got, 11)
	}
	if got := done.Usage.CompletionTokens; got != 7 {
		t.Errorf("completion_tokens = %d, want %d", got, 7)
	}
	// The total is precomputed here so every consumer sees the same sum.
	if got := done.Usage.TotalTokens; got != 18 {
		t.Errorf("total_tokens = %d, want %d", got, 18)
	}
}

func TestRunJSONNamesTheProviderAndModel(t *testing.T) {
	h := newHarness(t, newStub(t, "alpha"))

	r := h.run(t, "run", "--json", "hello")

	wantCode(t, r, 0)
	done := lastEventOfType(t, r.stdout, "done")
	if done.Provider != "groq" {
		t.Errorf("provider = %q, want %q", done.Provider, "groq")
	}
	if done.Model != "llama-3.1-8b-instant" {
		t.Errorf("model = %q, want %q", done.Model, "llama-3.1-8b-instant")
	}
}

func TestRunJSONFollowsTheModelFlag(t *testing.T) {
	h := newHarness(t, newStub(t, "alpha"))

	r := h.run(t, "--model", "llama-3.3-70b-versatile", "run", "--json", "hello")

	wantCode(t, r, 0)
	done := lastEventOfType(t, r.stdout, "done")
	if done.Model != "llama-3.3-70b-versatile" {
		t.Errorf("model = %q, want %q", done.Model, "llama-3.3-70b-versatile")
	}
}

// After a fallback the answer comes from another provider's model, and every
// event on the wire must say so. Stamping the session's own model — the one
// the user chose — would misattribute the whole answer.
func TestRunJSONNamesTheFallbackModelAfterAFallback(t *testing.T) {
	answering := newStub(t, "from the fallback")
	h := newHarness(t, answering)

	// The preferred provider is exhausted: every request is a 429, which
	// is the fallback trigger.
	exhausted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(exhausted.Close)

	path := writeConfig(t, h.dir, fmt.Sprintf(`fallbacks = ["gemini"]

[providers.groq]
base_url = %q
model = "llama-3.1-8b-instant"
api_key_env = %q

[providers.gemini]
base_url = %q
model = "gemini-fallback-model"
api_key_env = %q
`, exhausted.URL, testKeyEnv, answering.server.URL, testKeyEnv))

	r := runBare(t, "--config", path, "run", "--json", "hello")

	wantCode(t, r, 0)
	events := decodeNDJSON[wireEvent](t, r.stdout)
	if len(events) < 3 {
		t.Fatalf("want at least a notice, a delta and a done event, got %d\n--- stdout ---\n%s", len(events), r.stdout)
	}
	if events[0].Type != "notice" || events[0].Provider != "gemini" || events[0].Model != "gemini-fallback-model" {
		t.Errorf("first event = %+v, want a gemini notice carrying gemini-fallback-model", events[0])
	}
	for _, ev := range events[1:] {
		if ev.Provider != "gemini" {
			t.Errorf("%s event names provider %q, want gemini", ev.Type, ev.Provider)
		}
		if ev.Model != "gemini-fallback-model" {
			t.Errorf("%s event names model %q, want gemini-fallback-model", ev.Type, ev.Model)
		}
	}
	if got := joinDeltas(t, r.stdout); got != "from the fallback" {
		t.Errorf("deltas = %q, want the fallback's answer", got)
	}
}

func TestRunJSONEmitsADoneEventForAnEmptyAnswer(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "run", "--json", "say nothing")

	wantCode(t, r, 0)
	events := decodeNDJSON[wireEvent](t, r.stdout)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1\n--- stdout ---\n%s", len(events), r.stdout)
	}
	if events[0].Type != "done" {
		t.Errorf("event type = %q, want %q", events[0].Type, "done")
	}
}

func TestRunJSONReportsAMidStreamFailureAsAnEvent(t *testing.T) {
	h := newHarness(t, newStubWithFrames(t, []string{
		rolePrimingFrame(),
		contentFrame("partial "),
		frame("{this is not json"),
		doneFrame(),
	}))

	r := h.run(t, "run", "--json", "hello")

	wantCode(t, r, 1)
	// A consumer reading only stdout still learns what went wrong, and the
	// deltas that did arrive before the break are still there.
	errEvent := lastEventOfType(t, r.stdout, "error")
	wantContains(t, "error event", errEvent.Error, "decoding stream frame")
	if got := joinDeltas(t, r.stdout); got != "partial " {
		t.Errorf("deltas = %q, want %q", got, "partial ")
	}
}

func TestRunJSONStillReportsAMidStreamFailureOnStderr(t *testing.T) {
	h := newHarness(t, newStubWithFrames(t, []string{
		rolePrimingFrame(),
		frame("{this is not json"),
	}))

	r := h.run(t, "run", "--json", "hello")

	wantCode(t, r, 1)
	// The event stream is for the program; stderr and the exit code are for
	// the shell that started it.
	wantContains(t, "stderr", r.stderr, "yonderllm:", "decoding stream frame")
}

func TestRunPlainReportsAMidStreamFailureOnStderrOnly(t *testing.T) {
	h := newHarness(t, newStubWithFrames(t, []string{
		rolePrimingFrame(),
		contentFrame("partial "),
		frame("{this is not json"),
	}))

	r := h.run(t, "run", "hello")

	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr, "decoding stream frame")
	// Text already written is kept, closed off with a newline so the error
	// message does not land on the same line as the answer.
	if got := r.stdout; got != "partial \n" {
		t.Errorf("stdout = %q, want %q", got, "partial \n")
	}
}

func TestRunRejectsAnUnknownProvider(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.run(t, "--provider", "nope", "run", "--json", "hello")

	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr, "nope")
	// The session never came into being, so there was no stream to write an
	// error event into.
	if r.stdout != "" {
		t.Errorf("stdout = %q, want empty", r.stdout)
	}
	if h.stub.seen {
		t.Error("the backend was called for an unknown provider")
	}
}

func TestRunStreams(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.run(t, "run", "--json", "hello")

	wantCode(t, r, 0)
	if !h.stub.last.Stream {
		t.Error("the request did not ask for a stream")
	}
}

func TestRunHelpDescribesTheJSONContract(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.run(t, "run", "--help")

	wantCode(t, r, 0)
	wantContains(t, "help", r.stdout,
		"Send a single prompt and emit the result for a program to consume",
		"one JSON",
		"object per line",
		"standard input",
		"--json",
		"jq",
	)
}

func TestRunJSONReportsAToolCallBeforeItsResult(t *testing.T) {
	h := newHarness(t, newStubWithRounds(t, [][]string{
		toolCallRound("call_1", "read_file", `{"path":"run.go"}`),
		proseRound("done"),
	}))

	r := h.run(t, "run", "--json", "--mode", "code", "read run.go")

	wantCode(t, r, 0)
	calls := toolEvents(t, r.stdout)
	if len(calls) != 2 {
		t.Fatalf("want a call and a result, got %d tool events\n--- stdout ---\n%s",
			len(calls), r.stdout)
	}
	if calls[0].Type != "tool" || calls[1].Type != "tool_result" {
		t.Errorf("want tool then tool_result, got %q then %q", calls[0].Type, calls[1].Type)
	}
	if calls[0].Tool.ID != calls[1].Tool.ID {
		t.Errorf("the call and its result carry different ids: %q and %q",
			calls[0].Tool.ID, calls[1].Tool.ID)
	}
	if calls[0].Tool.ID != "call_1" {
		t.Errorf("tool id = %q, want %q", calls[0].Tool.ID, "call_1")
	}
	if calls[0].Tool.Name != "read_file" {
		t.Errorf("tool name = %q, want %q", calls[0].Tool.Name, "read_file")
	}
	if calls[0].Tool.Arguments != `{"path":"run.go"}` {
		t.Errorf("tool arguments = %q, want the model's raw JSON", calls[0].Tool.Arguments)
	}
}

// The arguments the model produced are never validated, so they travel as a
// string. Inlining them would let one malformed object break the line it rides
// on and take the rest of the stream with it.
func TestRunJSONCarriesToolArgumentsAsAString(t *testing.T) {
	h := newHarness(t, newStubWithRounds(t, [][]string{
		toolCallRound("call_1", "read_file", `{"path":"run.go"}`),
		proseRound("done"),
	}))

	r := h.run(t, "run", "--json", "--mode", "code", "read run.go")

	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, `"arguments":"{\"path\":\"run.go\"}"`)
}

func TestRunJSONReportsWhatAToolFound(t *testing.T) {
	h := newHarness(t, newStubWithRounds(t, [][]string{
		toolCallRound("call_1", "read_file", `{"path":"run.go"}`),
		proseRound("done"),
	}))

	r := h.run(t, "run", "--json", "--mode", "code", "read run.go")

	wantCode(t, r, 0)
	result := lastEventOfType(t, r.stdout, "tool_result")
	if result.Tool.Error != "" {
		t.Fatalf("reading a file that exists failed: %s", result.Tool.Error)
	}
	if !strings.Contains(result.Tool.Result, "run.go") {
		t.Errorf("the result does not name the file it read:\n%s", result.Tool.Result)
	}
}

// A tool that fails is reported, not hidden: the model is told and so is the
// consumer of the stream.
func TestRunJSONReportsAToolThatFailed(t *testing.T) {
	h := newHarness(t, newStubWithRounds(t, [][]string{
		toolCallRound("call_1", "read_file", `{}`),
		proseRound("sorry"),
	}))

	r := h.run(t, "run", "--json", "--mode", "code", "read nothing")

	wantCode(t, r, 0)
	result := lastEventOfType(t, r.stdout, "tool_result")
	if result.Tool.Error == "" {
		t.Fatalf("a call with no path was reported as a success:\n--- stdout ---\n%s", r.stdout)
	}
	if !strings.Contains(result.Tool.Error, "path") {
		t.Errorf("the error does not say which argument was missing: %q", result.Tool.Error)
	}
	if result.Tool.Result != "" {
		t.Errorf("a failed call carries a result as well: %q", result.Tool.Result)
	}
}

// A tool call is a step towards the answer, not part of it, so the plain form
// says nothing about it and the answer arrives exactly as it would have without
// the detour.
func TestRunKeepsPlainOutputFreeOfToolNoise(t *testing.T) {
	h := newHarness(t, newStubWithRounds(t, [][]string{
		toolCallRound("call_1", "read_file", `{"path":"run.go"}`),
		proseRound("done"),
	}))

	r := h.run(t, "run", "--mode", "code", "read run.go")

	wantCode(t, r, 0)
	if r.stdout != "done\n" {
		t.Errorf("stdout = %q, want just the answer", r.stdout)
	}
	wantNotContains(t, "stdout", r.stdout, "read_file", "call_1")
}

// A tool round reports no usage of its own, so the totals on the done event
// still describe the exchange rather than only its last leg.
func TestRunJSONStillReportsUsageAfterATool(t *testing.T) {
	h := newHarness(t, newStubWithRounds(t, [][]string{
		toolCallRound("call_1", "read_file", `{"path":"run.go"}`),
		proseRound("done"),
	}))

	r := h.run(t, "run", "--json", "--mode", "code", "read run.go")

	wantCode(t, r, 0)
	done := lastEventOfType(t, r.stdout, "done")
	if done.Usage == nil {
		t.Fatalf("the done event reports no usage\n--- stdout ---\n%s", r.stdout)
	}
	if done.Usage.TotalTokens != 18 {
		t.Errorf("total tokens = %d, want %d", done.Usage.TotalTokens, 18)
	}
}

// toolEvents returns the tool and tool_result events in the order they arrived,
// which is the order a consumer would have to make sense of them in.
func toolEvents(t *testing.T, stdout string) []wireEvent {
	t.Helper()

	var out []wireEvent
	for _, ev := range decodeNDJSON[wireEvent](t, stdout) {
		if ev.Type != "tool" && ev.Type != "tool_result" {
			continue
		}
		if ev.Tool == nil {
			t.Fatalf("a %q event carries no tool\n--- stdout ---\n%s", ev.Type, stdout)
		}
		out = append(out, ev)
	}
	return out
}

// joinDeltas concatenates the delta events, which is the answer text as a
// consumer of the JSON stream would reassemble it.
func joinDeltas(t *testing.T, stdout string) string {
	t.Helper()

	var b strings.Builder
	for _, ev := range decodeNDJSON[wireEvent](t, stdout) {
		if ev.Type == "delta" {
			b.WriteString(ev.Delta)
		}
	}
	return b.String()
}

// lastEventOfType returns the final event of the given type, failing the test
// if the stream never produced one.
func lastEventOfType(t *testing.T, stdout, kind string) wireEvent {
	t.Helper()

	events := decodeNDJSON[wireEvent](t, stdout)
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == kind {
			return events[i]
		}
	}
	t.Fatalf("no %q event on stdout\n--- stdout ---\n%s", kind, stdout)
	return wireEvent{}
}
