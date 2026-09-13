package shell

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"yonderllm/internal/perm"
)

// sep is the separator this platform puts in a path, so that a test can state
// the shape of an answer without assuming which platform produced it.
var sep = string(filepath.Separator)

// runner builds a Runner over a scratch directory, which is where a command
// that writes something should land rather than in the repository the tests
// are running from.
func runner(t *testing.T, mode perm.Mode) *Runner {
	t.Helper()

	return Open(t.TempDir(), perm.New(mode))
}

// goTool is the one program these tests assume, on the grounds that a test
// suite for a Go project is already being run by it. It reports facts about
// itself without touching anything, so it stands in for an ordinary command
// without needing a fixture to be built first.
const goTool = "go"

// deniedFor returns the refusal a mode produced, failing the test if the mode
// let the command through or objected to something else.
func deniedFor(t *testing.T, mode perm.Mode) *perm.DeniedError {
	t.Helper()

	_, err := runner(t, mode).Run(context.Background(), []string{goTool, "env", "GOOS"})

	var denied *perm.DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("running a command in %s mode: %v, want a *perm.DeniedError", mode, err)
	}
	return denied
}

// A mode that forbids execution must not be talked round by this package. The
// refusal is the policy's own error rather than a message invented here, so
// the caller can tell a mode that said no from a command that failed.
func TestRunRefusesModesWithoutExec(t *testing.T) {
	for _, mode := range []perm.Mode{perm.Chat, perm.Code} {
		t.Run(mode.String(), func(t *testing.T) {
			denied := deniedFor(t, mode)
			if denied.Mode != mode {
				t.Errorf("refusal names mode %s, want %s", denied.Mode, mode)
			}
			if denied.Action != perm.Exec {
				t.Errorf("refusal names action %s, want %s", denied.Action, perm.Exec)
			}
		})
	}
}

// Agent mode answers Ask rather than Allow, and Ask is a question this package
// has nobody to put. Refusing it here would leave no mode able to run anything
// even once the user has agreed, so the seam passes it through and the caller
// that owns an interface settles it first.
func TestRunAllowsAgentMode(t *testing.T) {
	result, err := runner(t, perm.Agent).Run(context.Background(), []string{goTool, "env", "GOOS"})
	if err != nil {
		t.Fatalf("running a command in agent mode: %v", err)
	}
	if result.Code != 0 {
		t.Errorf("exit code %d, want 0", result.Code)
	}
	if strings.TrimSpace(result.Output) == "" {
		t.Error("the command produced no output, want the name of an operating system")
	}
	if result.TimedOut {
		t.Error("a command that finished at once was reported as timing out")
	}
	if result.Dropped != 0 {
		t.Errorf("%d bytes dropped, want none from a short command", result.Dropped)
	}
}

// A command that runs and fails has answered the caller: a failing build is
// the information that was wanted, not a malfunction of this package. So the
// exit status and the complaint come back as a result, and err stays nil.
func TestRunReportsFailureAsResult(t *testing.T) {
	result, err := runner(t, perm.Agent).Run(context.Background(), []string{goTool, "vet", "-bogusflag"})
	if err != nil {
		t.Fatalf("a command that exited non-zero returned an error: %v", err)
	}
	if result.Code == 0 {
		t.Error("exit code 0, want a non-zero status from a rejected flag")
	}
	if !strings.Contains(result.Output, "bogusflag") {
		t.Errorf("output %q does not mention the flag that was refused", result.Output)
	}
}

// Nothing ran, so there is no exit status to report and a zero-valued result
// would read as success. This is the case the package calls an error.
func TestRunFailsWhenNothingStarts(t *testing.T) {
	_, err := runner(t, perm.Agent).Run(context.Background(), []string{"yonderllm-no-such-program"})
	if err == nil {
		t.Fatal("running a program that does not exist succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "yonderllm-no-such-program") {
		t.Errorf("error %q does not name the program that could not be started", err)
	}
}

// A reader who interrupts is obeyed, and is told they were the reason. The
// timeout this package adds is a different answer and must not be confused
// with the caller's own.
func TestRunObeysCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runner(t, perm.Agent).Run(ctx, []string{goTool, "env", "GOOS"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("running under a cancelled context: %v, want context.Canceled", err)
	}
}

// A command is not a way out of the tree the user opened. Both the plain
// statement of an outside path and the climb through the tree are refused, and
// the refusal says which command it was about.
func TestRunRefusesProgramsOutsideTheWorkspace(t *testing.T) {
	for _, name := range []string{"../evil", "sub/../../evil"} {
		t.Run(name, func(t *testing.T) {
			_, err := runner(t, perm.Agent).Run(context.Background(), []string{name})
			if err == nil {
				t.Fatalf("running %s succeeded, want a refusal", name)
			}
			if !strings.Contains(err.Error(), "outside the workspace") {
				t.Errorf("error %q does not say the program is outside the workspace", err)
			}
		})
	}
}

// program is what stands between a vector and a process, so its answers are
// checked directly rather than only through what a command happened to do.
func TestProgram(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
		bad  string
	}{
		{name: "an empty vector names nothing", args: nil, bad: "no command named"},
		{name: "blank space names nothing", args: []string{"   "}, bad: "no command named"},
		{name: "a bare name is left for PATH", args: []string{"go"}, want: "go"},
		{name: "a climb out is refused", args: []string{"../go"}, bad: "outside the workspace"},
		{name: "a path in the project is kept relative", args: []string{"scripts/build"}, want: "." + sep + "scripts" + sep + "build"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := program(test.args)
			switch {
			case test.bad != "":
				if err == nil {
					t.Fatalf("program(%q) = %q, want an error", test.args, got)
				}
				if !strings.Contains(err.Error(), test.bad) {
					t.Errorf("program(%q) failed with %q, want it to mention %q", test.args, err, test.bad)
				}
			case err != nil:
				t.Fatalf("program(%q) failed: %v", test.args, err)
			case got != test.want:
				t.Errorf("program(%q) = %q, want %q", test.args, got, test.want)
			}
		})
	}
}

// A rooted name is refused in either platform's spelling, on either platform.
// The vector is text a model wrote, not a path the host produced, so a build
// must not disagree with its neighbour about what escapes the project: on
// Windows filepath.IsAbs says no to /usr/bin/rm, and a check that trusted it
// would have handed the name back as a project-relative one.
func TestProgramRefusesRootedPaths(t *testing.T) {
	names := []string{
		"/usr/bin/rm",
		`C:\Windows\System32\cmd.exe`,
		`\Windows\System32\cmd.exe`,
		`\\host\share\tool.exe`,
		`C:tool.exe`,
		"/bin/sh",
	}
	for _, name := range names {
		got, err := program([]string{name})
		if err == nil {
			t.Errorf("program(%q) = %q, want a refusal", name, got)
			continue
		}
		if !strings.Contains(err.Error(), "outside the workspace") {
			t.Errorf("program(%q) failed with %q, want it to say the name is outside the workspace", name, err)
		}
	}
}

// Destructive is the half of the contract that survives auto-approval, so the
// table is where the promise to confirm is actually kept. It errs towards yes:
// the cases that must be caught outnumber the cases that must be let past, and
// a missed one is not a question the user gets to answer.
func TestDestructive(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{args: nil, want: false},
		{args: []string{"ls"}, want: false},
		{args: []string{"go", "build", "./..."}, want: false},
		{args: []string{"go", "test", "./..."}, want: false},
		{args: []string{"git", "status"}, want: false},
		{args: []string{"git", "log", "--oneline"}, want: false},
		{args: []string{"rm", "notes.txt"}, want: true},
		{args: []string{"rm", "-rf", "build"}, want: true},
		{args: []string{"/usr/bin/rm", "notes.txt"}, want: true},
		{args: []string{"rm.exe", "notes.txt"}, want: true},
		{args: []string{"RM", "notes.txt"}, want: true},
		{args: []string{"scripts/rm", "notes.txt"}, want: true},
		{args: []string{"sudo", "apt", "update"}, want: true},
		{args: []string{"git", "push"}, want: true},
		{args: []string{"git", "reset", "--hard"}, want: true},
		{args: []string{"git", "clean", "-fd"}, want: true},
		{args: []string{"npm", "publish"}, want: true},
		{args: []string{"terraform", "destroy"}, want: true},
		{args: []string{"kubectl", "delete", "pod", "web"}, want: true},
		{args: []string{"go", "clean", "-cache"}, want: true},
		{args: []string{"tidy", "--force"}, want: true},
		{args: []string{"gzip", "-f", "notes.txt"}, want: true},
		{args: []string{"tool", "-force"}, want: true},
		{args: []string{"tool", "/f"}, want: true},
		{args: []string{"tool", "--delete"}, want: true},
		{args: []string{"tool", "-delete"}, want: true},
		{args: []string{"tool", "--no-preserve-root"}, want: true},
	}
	for _, test := range tests {
		if got := Destructive(test.args); got != test.want {
			t.Errorf("Destructive(%q) = %v, want %v", test.args, got, test.want)
		}
	}
}

// The cap exists so that a command printing without end cannot exhaust memory
// before its timeout arrives. What it keeps must stay under the limit and what
// it discarded must still be counted, because a result that was quietly cut
// short reads as a command that simply said less.
func TestCappedCountsWhatItDropped(t *testing.T) {
	out := &capped{}

	chunk := strings.Repeat("x", 4096)
	written := 0
	for len(out.buf) < maxOutputBytes || out.over == 0 {
		n, err := out.Write([]byte(chunk))
		if err != nil {
			t.Fatalf("writing to the cap failed: %v", err)
		}
		if n != len(chunk) {
			t.Fatalf("short write of %d bytes, want %d; a command must not be told its output failed", n, len(chunk))
		}
		written += len(chunk)
	}

	if len(out.buf) > maxOutputBytes {
		t.Errorf("kept %d bytes, want no more than %d", len(out.buf), maxOutputBytes)
	}
	if len(out.buf)+out.over != written {
		t.Errorf("kept %d and dropped %d, want them to sum to the %d written", len(out.buf), out.over, written)
	}
	if out.text() != string(out.buf) {
		t.Error("the text read back differs from what was kept")
	}
}

// Current follows the working directory, which is the project the user started
// the program in and so the one they mean by an unqualified command.
func TestCurrentUsesTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	r, err := Current(perm.New(perm.Agent))
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		want = dir
	}
	got, err := filepath.EvalSymlinks(r.Dir())
	if err != nil {
		got = r.Dir()
	}
	if got != want {
		t.Errorf("the runner reports %s, want the directory the user started in, %s", got, want)
	}
}
