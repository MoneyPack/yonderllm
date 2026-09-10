package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// childEnv marks a re-executed copy of this test binary as the child that
	// should run main() instead of the test suite.
	childEnv = "YONDERLLM_MAIN_CHILD"
	// childArgs carries the argv the child should hand to the command. It
	// travels in the environment rather than on the command line so the
	// child's real os.Args stays free of test flags.
	childArgs = "YONDERLLM_MAIN_ARGS"
	// argSep joins the child's arguments. A unit separator cannot appear in a
	// flag or a command name, so no argument needs escaping.
	argSep = "\x1f"
)

// TestMain doubles as the entry point for the child processes the tests spawn.
// main() ends in os.Exit, which would tear down a test binary mid-run, so the
// only honest way to exercise it is from the outside: the parent re-executes
// this same binary with childEnv set, and the child becomes the command.
func TestMain(m *testing.M) {
	if os.Getenv(childEnv) != "1" {
		os.Exit(m.Run())
	}

	os.Args = append([]string{"yonderllm"}, decodeArgs(os.Getenv(childArgs))...)
	main()
}

func decodeArgs(encoded string) []string {
	if encoded == "" {
		return nil
	}
	return strings.Split(encoded, argSep)
}

// result is what a child process left behind.
type result struct {
	code   int
	stdout string
	stderr string
}

// firstLine returns the leading line of the stream the command was expected to
// write on, preferring stdout and falling back to stderr.
func (r result) firstLine() string {
	stream := r.stdout
	if strings.TrimSpace(stream) == "" {
		stream = r.stderr
	}
	line, _, _ := strings.Cut(strings.ReplaceAll(stream, "\r\n", "\n"), "\n")
	return strings.TrimSpace(line)
}

// runMain re-executes the test binary as the yonderllm command and reports the
// exit status main() produced along with everything it wrote.
func runMain(t *testing.T, args ...string) result {
	t.Helper()

	cmd := exec.Command(os.Args[0])
	cmd.Env = childEnviron(t, args)
	// An empty stdin keeps commands that would otherwise read a piped prompt
	// from blocking, and keeps the TUI from trying to claim a terminal.
	cmd.Stdin = strings.NewReader("")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running %v: %v", args, err)
		}
		code = exit.ExitCode()
	}

	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// childEnviron copies the current environment, then shadows everything that
// could steer a result: the config path is aimed at an empty temporary
// directory and every provider key is blanked, so no test can read the
// developer's config or reach a live API.
func childEnviron(t *testing.T, args []string) []string {
	t.Helper()

	overrides := map[string]string{
		childEnv:             "1",
		childArgs:            strings.Join(args, argSep),
		"YONDERLLM_CONFIG":   filepath.Join(t.TempDir(), "config.toml"),
		"YONDERLLM_PROVIDER": "",
		"YONDERLLM_MODEL":    "",
		"YONDERLLM_MODE":     "",
		"GROQ_API_KEY":       "",
		"GEMINI_API_KEY":     "",
		"OPENROUTER_API_KEY": "",
		"SURPLUS_API_KEY":    "",
	}

	parent := os.Environ()
	env := make([]string, 0, len(parent)+len(overrides))
	for _, entry := range parent {
		key, _, ok := strings.Cut(entry, "=")
		if _, shadowed := overrides[key]; ok && shadowed {
			continue
		}
		env = append(env, entry)
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

// TestMainExitCodes pins the contract the shell sees. main() forwards whatever
// cli.Main returns, so a command that succeeds must exit 0 and a command that
// fails must exit non-zero -- the part of the binary no in-process test can
// reach.
func TestMainExitCodes(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantLine string
	}{
		{
			name:     "version",
			args:     []string{"--version"},
			wantCode: 0,
			wantLine: "yonderllm ",
		},
		{
			name:     "help",
			args:     []string{"--help"},
			wantCode: 0,
			wantLine: "yonderllm is a terminal client for language models that run somewhere else.",
		},
		{
			name:     "subcommand help",
			args:     []string{"models", "--help"},
			wantCode: 0,
			wantLine: "List the models available from a provider, cheapest tier first.",
		},
		{
			name:     "unknown command",
			args:     []string{"definitely-not-a-command"},
			wantCode: 1,
			wantLine: `yonderllm: unknown command "definitely-not-a-command" for "yonderllm"`,
		},
		{
			name:     "ask with no prompt",
			args:     []string{"ask"},
			wantCode: 1,
			wantLine: "yonderllm: no prompt: pass one as an argument or pipe it on stdin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runMain(t, tt.args...)

			if got.code != tt.wantCode {
				t.Errorf("exit code: got %d, want %d\nstdout: %s\nstderr: %s",
					got.code, tt.wantCode, got.stdout, got.stderr)
			}
			if line := got.firstLine(); !strings.HasPrefix(line, tt.wantLine) {
				t.Errorf("first line: got %q, want prefix %q", line, tt.wantLine)
			}
		})
	}
}

// TestMainFailureGoesToStderr keeps diagnostics off stdout, so a caller can
// pipe a model's answer somewhere without catching an error message in it.
func TestMainFailureGoesToStderr(t *testing.T) {
	got := runMain(t, "definitely-not-a-command")

	if strings.TrimSpace(got.stderr) == "" {
		t.Error("a failing command wrote nothing to stderr")
	}
	if strings.TrimSpace(got.stdout) != "" {
		t.Errorf("a failing command wrote to stdout: %q", got.stdout)
	}
}
