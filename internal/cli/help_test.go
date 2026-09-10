// Tests for the overridden help, usage, and completion output.
//
// Help text is a product surface here, not an afterthought, so these tests
// guard both its content and its column alignment. Alignment is asserted on
// exact lines because a regression in the padding helpers would otherwise slip
// through a substring check.
package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootHelpDescribesTheProduct(t *testing.T) {
	r := runBare(t, "--help")

	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout,
		"yonderllm is a terminal client for language models that run somewhere else.",
		"Run without a subcommand to open the interactive session.",
		"Usage",
		"yonderllm [flags]",
		"yonderllm <command> [flags]",
		"Commands",
		"Flags",
		`Run "yonderllm <command> --help" for detail on a command.`,
	)
}

func TestRootHelpListsEverySubcommand(t *testing.T) {
	r := runBare(t, "--help")

	wantCode(t, r, 0)
	for _, name := range []string{"ask", "completion", "config", "help", "models", "providers", "run"} {
		if !strings.Contains(r.stdout, "\n  "+name) {
			t.Errorf("root help does not list the %q command\n--- stdout ---\n%s", name, r.stdout)
		}
	}
}

// The default cobra section headings are replaced wholesale; if any leak back
// in, the templates have stopped being applied.
func TestRootHelpUsesOurSectionHeadings(t *testing.T) {
	r := runBare(t, "--help")

	wantCode(t, r, 0)
	wantNotContains(t, "stdout", r.stdout,
		"Usage:",
		"Available Commands:",
		"Flags:",
		"Additional help topics:",
		"Use \"yonderllm [command] --help\"",
	)
}

// Command and flag columns are padded by our own helpers, so their alignment
// is a regression risk worth pinning exactly.
func TestHelpColumnsAreAligned(t *testing.T) {
	r := runBare(t, "--help")
	wantCode(t, r, 0)

	for _, line := range []string{
		"  ask         Send one prompt and stream the answer",
		"  completion  Generate a shell completion script",
		"  providers   Show the configured providers and their credentials",
		"  -p, --provider string   provider to use first (groq, gemini, openrouter, ...)",
		"  -h, --help              show help for this command",
	} {
		if !strings.Contains(r.stdout, line) {
			t.Errorf("misaligned or missing line:\n%q\n--- stdout ---\n%s", line, r.stdout)
		}
	}
}

// No line of help should carry trailing whitespace: that is what the custom
// trimTrailingWhitespaces function exists to prevent.
func TestHelpHasNoTrailingWhitespace(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"help", "ask"},
		{"help", "config"},
		{"completion", "--help"},
	} {
		r := runBare(t, args...)
		wantCode(t, r, 0)

		for i, line := range strings.Split(r.stdout, "\n") {
			if line != strings.TrimRight(line, " \t") {
				t.Errorf("%v: line %d has trailing whitespace: %q", args, i+1, line)
			}
		}
	}
}

// A bare invocation with no TTY still needs to be helpful rather than silent.
func TestBareRootPrintsHelp(t *testing.T) {
	r := runBare(t)

	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, "Usage", "Commands", "ask")
}

// A nil argument slice must mean "no arguments", not "inherit whatever flags
// the host process was launched with". Cobra's SetArgs treats nil as a request
// to read os.Args, so Execute normalises it; without that, running the suite
// under `go test -coverprofile=...` made the CLI try to parse ".out".
func TestNilArgsDoesNotInheritProcessArgs(t *testing.T) {
	var out, errOut bytes.Buffer

	code := Execute(nil, strings.NewReader(""), &out, &errOut)

	if code != 0 {
		t.Errorf("exit code = %d, want 0\n--- stderr ---\n%s", code, errOut.String())
	}
	wantContains(t, "stdout", out.String(), "Usage", "Commands")
	wantNotContains(t, "stderr", errOut.String(), "unknown command")
}

func TestHelpSubcommandRendersCommandHelp(t *testing.T) {
	r := runBare(t, "help", "ask")

	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout,
		"Send a single prompt to the active provider",
		"Usage",
		"yonderllm ask [prompt] [flags]",
		"Examples",
		`yonderllm ask "explain the borrow checker"`,
		"Global flags",
	)
}

// Examples are dedented and re-indented by the indent helper, so verify the
// block keeps a uniform two-space indent rather than the source indentation.
func TestExamplesAreIndentedUniformly(t *testing.T) {
	r := runBare(t, "help", "ask")
	wantCode(t, r, 0)

	for _, line := range []string{
		`  yonderllm ask "explain the borrow checker"`,
		`  git diff | yonderllm ask "write a commit message for this diff"`,
		`  yonderllm -p gemini ask "summarise the CAP theorem"`,
	} {
		if !strings.Contains(r.stdout, line) {
			t.Errorf("example line not indented as expected:\n%q\n--- stdout ---\n%s", line, r.stdout)
		}
	}
}

func TestHelpOnUnknownTopicFails(t *testing.T) {
	r := runBare(t, "help", "nonesuch")

	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr, `unknown help topic "nonesuch"`)
}

func TestUnknownCommandFails(t *testing.T) {
	r := runBare(t, "nonesuch")

	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr, "nonesuch")
}

func TestVersionFlagPrintsVersion(t *testing.T) {
	r := runBare(t, "--version")

	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, "yonderllm", Version)
}

func TestCompletionHelpListsShells(t *testing.T) {
	r := runBare(t, "completion", "--help")

	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, "bash", "zsh", "fish", "powershell")
}

// Each generator must produce a script that a shell would actually accept,
// which at minimum means non-empty output naming the binary.
func TestCompletionScriptsGenerate(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		r := runBare(t, "completion", shell)

		wantCode(t, r, 0)
		if len(r.stdout) == 0 {
			t.Errorf("%s completion produced no output", shell)
			continue
		}
		wantContains(t, shell+" completion", r.stdout, "yonderllm")
	}
}

// The --no-descriptions flag must be accepted by every shell subcommand, and
// must actually change the generated script.
func TestCompletionNoDescriptionsIsAccepted(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		with := runBare(t, "completion", shell)
		without := runBare(t, "completion", shell, "--no-descriptions")

		wantCode(t, with, 0)
		wantCode(t, without, 0)

		if with.stdout == without.stdout {
			t.Errorf("%s completion ignores --no-descriptions", shell)
		}
	}
}

func TestCompletionRejectsUnknownShell(t *testing.T) {
	r := runBare(t, "completion", "tcsh")

	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr, "tcsh")
}
