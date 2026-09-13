// Tests for --yes, the flag a script uses to say in advance that it accepts
// the actions agent mode would otherwise stop to ask about.
//
// The interesting cases are all at the seam between the command line and the
// permission policy: which capabilities reach the model at all, what a model
// is told when one does not, and which actions --yes still cannot wave
// through. They are written through the run subcommand because its --json
// stream reports tool calls and their results, so what the model saw is
// visible without a terminal.
package cli

import (
	"runtime"
	"strings"
	"testing"
)

// The point of the flag: a non-interactive agent run with --yes may execute a
// command, with nobody to ask and nothing to prompt.
func TestRunWithYesRunsACommandWithoutAsking(t *testing.T) {
	h := newHarness(t, newStubWithRounds(t, [][]string{
		toolCallRound("call_1", "run_command", `{"command":["go","env","GOOS"]}`),
		proseRound("done"),
	}))

	r := h.run(t, "run", "--json", "--mode", "agent", "--yes", "which os is this")

	wantCode(t, r, 0)
	result := lastEventOfType(t, r.stdout, "tool_result")
	if result.Tool.Error != "" {
		t.Fatalf("the command was refused under --yes: %q", result.Tool.Error)
	}
	if !strings.Contains(result.Tool.Result, runtime.GOOS) {
		t.Errorf("the result does not carry the command's output: %q", result.Tool.Result)
	}
}

// Without the flag the same run has no way to put the question to anyone, so
// the capability is withheld rather than offered and then always refused. The
// model learns that from the tool result, which is the only channel it has.
func TestRunWithoutYesWithholdsTheCommandTool(t *testing.T) {
	h := newHarness(t, newStubWithRounds(t, [][]string{
		toolCallRound("call_1", "run_command", `{"command":["go","env","GOOS"]}`),
		proseRound("sorry"),
	}))

	r := h.run(t, "run", "--json", "--mode", "agent", "which os is this")

	wantCode(t, r, 0)
	result := lastEventOfType(t, r.stdout, "tool_result")
	if result.Tool.Error == "" {
		t.Fatalf("a withheld tool reported a success:\n--- stdout ---\n%s", r.stdout)
	}
	wantContains(t, "the tool error", result.Tool.Error, `no tool named "run_command" is available`)
}

// --yes relaxes agent mode; it does not redefine the others. Chat and code have
// limits that are not negotiable, so the flag is refused outright rather than
// accepted and quietly ignored.
func TestYesIsRejectedOutsideAgentMode(t *testing.T) {
	for _, mode := range []string{"chat", "code"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, newStub(t, "unreachable"))

			r := h.run(t, "run", "--mode", mode, "--yes", "hello")

			wantCode(t, r, 1)
			wantContains(t, "stderr", r.stderr, "--yes", "agent", mode)
			if h.stub.seen {
				t.Error("the request was sent anyway; the flag should be refused before any model is reached")
			}
		})
	}
}

// The flag says the caller accepts the ordinary work of agent mode, not that
// nobody need ever be asked again. A destructive command still requires a
// confirmation, and where there is nobody to confirm it, it does not run.
func TestYesDoesNotWaveThroughADestructiveCommand(t *testing.T) {
	h := newHarness(t, newStubWithRounds(t, [][]string{
		toolCallRound("call_1", "run_command", `{"command":["git","push","--force"]}`),
		proseRound("sorry"),
	}))

	r := h.run(t, "run", "--json", "--mode", "agent", "--yes", "push my work")

	wantCode(t, r, 0)
	result := lastEventOfType(t, r.stdout, "tool_result")
	if result.Tool.Error == "" {
		t.Fatalf("a forced push went through unconfirmed:\n--- stdout ---\n%s", r.stdout)
	}
	wantContains(t, "the tool error", result.Tool.Error, "no way to ask the user about git")
}
