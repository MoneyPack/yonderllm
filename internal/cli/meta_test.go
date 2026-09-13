// Tests for interactive-stdin detection and the help/completion metacommands.
//
// help_test.go already pins what these commands print. What it leaves
// unguarded is the behaviour behind the printing: whether a reader counts as
// a terminal, which commands the help command offers as completions, and
// whether the completion commands reject stray arguments. Those are the parts
// a refactor can break silently, because the rendered help looks identical
// either way.

package cli

import (
	"bytes"
	"io"
	"os"
	"sort"
	"strings"
	"testing"
)

// interactiveStdin must answer no for every reader a test or a shell pipeline
// can hand it. Only a real character device is a terminal, and none of these
// are — including the two nil shapes, which must not panic.
func TestInteractiveStdinRejectsNonTerminals(t *testing.T) {
	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error: %v", err)
	}
	defer pipeR.Close()
	defer pipeW.Close()

	cases := []struct {
		name   string
		reader io.Reader
	}{
		{"strings reader", strings.NewReader("")},
		{"bytes buffer", &bytes.Buffer{}},
		{"nil interface", nil},
		{"nil file", (*os.File)(nil)},
		{"os pipe", pipeR},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if interactiveStdin(tc.reader) {
				t.Errorf("interactiveStdin(%s) = true, want false", tc.name)
			}
		})
	}
}

// completionNames pulls the command names out of a __complete response,
// dropping the trailing directive line and each entry's description.
func completionNames(t *testing.T, stdout string) []string {
	t.Helper()
	var names []string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		name, _, _ := strings.Cut(line, "\t")
		names = append(names, name)
	}
	return names
}

// "yonderllm help co<TAB>" must offer exactly the commands whose names start
// with that prefix, each with its one-line summary attached. The completion
// runs through cobra's hidden __complete command, which is how a real shell
// asks; calling ValidArgsFunction directly would pin a signature that cobra
// reserves the right to change.
func TestHelpCompletionOffersMatchingCommands(t *testing.T) {
	r := runBare(t, "__complete", "help", "co")
	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout,
		"completion\tGenerate a shell completion script",
		"config",
	)

	got := completionNames(t, r.stdout)
	sort.Strings(got)
	if joined := strings.Join(got, " "); joined != "completion config" {
		t.Errorf("completions for %q = %q, want %q", "co", joined, "completion config")
	}
}

// Completing past a command that does not exist must offer nothing rather
// than falling back to the whole command list or to filenames.
func TestHelpCompletionOnUnknownTopicOffersNothing(t *testing.T) {
	r := runBare(t, "__complete", "help", "nonsense", "")
	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, ":4")
	wantNotContains(t, "stdout", r.stdout, "config", "ask")
	if names := completionNames(t, r.stdout); len(names) != 0 {
		t.Errorf("got completions %v, want none", names)
	}
}

// "yonderllm completion" on its own names no shell, so it prints its help
// instead of guessing — and that help has to list the shells on offer.
func TestBareCompletionPrintsHelp(t *testing.T) {
	r := runBare(t, "completion")
	wantCode(t, r, 0)
	wantContains(t, "stdout", r.stdout, "bash", "zsh", "fish", "powershell")
}

// A completion script is useless without the line that installs it, so each
// shell's help must carry its own copy-pasteable instructions.
func TestCompletionShellHelpShowsInstallLines(t *testing.T) {
	cases := []struct {
		shell string
		lines []string
	}{
		{"bash", []string{
			"source <(yonderllm completion bash)",
			"yonderllm completion bash > /etc/bash_completion.d/yonderllm",
		}},
		{"zsh", []string{
			`yonderllm completion zsh > "${fpath[1]}/_yonderllm"`,
		}},
		{"fish", []string{
			"yonderllm completion fish | source",
			"yonderllm completion fish > ~/.config/fish/completions/yonderllm.fish",
		}},
		{"powershell", []string{
			"yonderllm completion powershell | Out-String | Invoke-Expression",
			"yonderllm completion powershell >> $PROFILE",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.shell, func(t *testing.T) {
			r := runBare(t, "completion", tc.shell, "--help")
			wantCode(t, r, 0)
			wantContains(t, "stdout", r.stdout, tc.lines...)
		})
	}
}

// The shell subcommands take no arguments. A stray one is a typo worth
// reporting, not something to silently ignore while printing a script.
func TestCompletionShellRejectsExtraArguments(t *testing.T) {
	r := runBare(t, "completion", "bash", "extra")
	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr, "extra")
}
