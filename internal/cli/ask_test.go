// Tests for the ask command.
//
// ask is the command a human types, so what matters is the shape of what
// lands on the terminal: answer text on stdout and nothing else, notices kept
// out of the way on stderr, and a trailing newline so the shell prompt does
// not start mid-line. The prompt-reading rules — argv, then stdin — are
// exercised here too, since readPrompt is shared with run.
package cli

import (
	"strings"
	"testing"
)

func TestAskStreamsTheAnswer(t *testing.T) {
	h := newHarness(t, newStub(t, "the borrow checker ", "tracks lifetimes"))

	r := h.run(t, "ask", "explain the borrow checker")

	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, "the borrow checker tracks lifetimes")
}

func TestAskEndsTheAnswerWithANewline(t *testing.T) {
	h := newHarness(t, newStub(t, "no trailing newline here"))

	r := h.run(t, "ask", "hello")

	wantCode(t, r, 0)
	if !strings.HasSuffix(r.stdout, "\n") {
		t.Errorf("stdout does not end with a newline\n--- stdout ---\n%q", r.stdout)
	}
	// Exactly one: the answer itself carried none, so the command should not
	// have padded it further.
	if got := r.stdout; got != "no trailing newline here\n" {
		t.Errorf("stdout = %q, want %q", got, "no trailing newline here\n")
	}
}

func TestAskWritesNothingWhenTheAnswerIsEmpty(t *testing.T) {
	h := newHarness(t, newStub(t))

	r := h.run(t, "ask", "say nothing")

	wantCode(t, r, 0)
	if r.stdout != "" {
		t.Errorf("stdout = %q, want empty", r.stdout)
	}
}

func TestAskJoinsMultipleArguments(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.run(t, "ask", "explain", "the", "borrow", "checker")

	wantCode(t, r, 0)
	wantLastUserMessage(t, h, "explain the borrow checker")
}

func TestAskReadsThePromptFromStdin(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.runWithInput(t, "write a commit message\n", "ask")

	wantCode(t, r, 0)
	wantLastUserMessage(t, h, "write a commit message")
}

func TestAskPrefersArgumentsOverStdin(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.runWithInput(t, "from stdin", "ask", "from argv")

	wantCode(t, r, 0)
	wantLastUserMessage(t, h, "from argv")
}

func TestAskTrimsThePrompt(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.runWithInput(t, "\n\n  spaced out  \n\n", "ask")

	wantCode(t, r, 0)
	wantLastUserMessage(t, h, "spaced out")
}

func TestAskRejectsAnEmptyPrompt(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.runWithInput(t, "", "ask")

	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr, "no prompt")
	if h.stub.seen {
		t.Error("the backend was called despite there being no prompt")
	}
}

func TestAskRejectsAWhitespacePrompt(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.runWithInput(t, "   \n\t\n", "ask")

	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr, "no prompt")
}

func TestAskRejectsABlankArgument(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.run(t, "ask", "   ")

	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr, "no prompt")
}

func TestAskSendsTheConfiguredModel(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.run(t, "ask", "hello")

	wantCode(t, r, 0)
	if got := h.stub.last.Model; got != "llama-3.1-8b-instant" {
		t.Errorf("model = %q, want %q", got, "llama-3.1-8b-instant")
	}
}

func TestAskFollowsTheModelFlag(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.run(t, "--model", "llama-3.3-70b-versatile", "ask", "hello")

	wantCode(t, r, 0)
	if got := h.stub.last.Model; got != "llama-3.3-70b-versatile" {
		t.Errorf("model = %q, want %q", got, "llama-3.3-70b-versatile")
	}
}

func TestAskStreams(t *testing.T) {
	h := newHarness(t, newStub(t, "one"))

	r := h.run(t, "ask", "hello")

	wantCode(t, r, 0)
	if !h.stub.last.Stream {
		t.Error("the request did not ask for a stream")
	}
}

func TestAskKeepsStdoutPure(t *testing.T) {
	h := newHarness(t, newStub(t, "just the answer"))

	r := h.run(t, "ask", "hello")

	wantCode(t, r, 0)
	// No banner, no provider name, no JSON: a pipe should receive only what
	// the model said.
	wantNotContains(t, "stdout", r.stdout, "yonderllm:", "groq", "{")
}

func TestAskIsSilentOnStderrWhenNothingGoesWrong(t *testing.T) {
	h := newHarness(t, newStub(t, "fine"))

	r := h.run(t, "ask", "hello")

	wantCode(t, r, 0)
	if r.stderr != "" {
		t.Errorf("stderr = %q, want empty", r.stderr)
	}
}

func TestAskRejectsAnUnknownProvider(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.run(t, "--provider", "nope", "ask", "hello")

	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr, "nope")
	if h.stub.seen {
		t.Error("the backend was called for an unknown provider")
	}
}

func TestAskAcceptsTheQuietFlag(t *testing.T) {
	h := newHarness(t, newStub(t, "quiet answer"))

	r := h.run(t, "ask", "--quiet", "hello")

	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, "quiet answer")
	if r.stderr != "" {
		t.Errorf("stderr = %q, want empty", r.stderr)
	}
}

func TestAskHelpDescribesTheStdinPath(t *testing.T) {
	h := newHarness(t, newStub(t, "ok"))

	r := h.run(t, "ask", "--help")

	wantCode(t, r, 0)
	wantContains(t, "help", r.stdout,
		"Send a single prompt to the active provider",
		"standard input",
		"-q, --quiet",
		"git diff | yonderllm ask",
	)
}

// wantLastUserMessage asserts on the final message the backend received, which
// is the prompt as the session assembled it.
func wantLastUserMessage(t *testing.T, h *harness, want string) {
	t.Helper()

	msgs := h.stub.last.Messages
	if len(msgs) == 0 {
		t.Fatal("the backend received no messages")
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" {
		t.Errorf("last message role = %q, want %q", last.Role, "user")
	}
	if last.Content != want {
		t.Errorf("last message content = %q, want %q", last.Content, want)
	}
}
