package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoneyPack/yonderllm/internal/perm"
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

// Every entry here once ran unprompted under --yes, because the table judged
// argv and the dangerous part was inside a string, behind an interpreter, or
// spelled with a Windows extension. Each is a bypass the audit named, kept as
// a regression so that a future trim of the tables cannot quietly reopen one.
func TestDestructiveCatchesInterpretersAndLaunchers(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"sh -c", []string{"sh", "-c", "rm -rf ."}},
		{"bash script", []string{"bash", "x.sh"}},
		{"zsh", []string{"zsh", "-c", "true"}},
		{"dash", []string{"dash", "x.sh"}},
		{"fish", []string{"fish", "-c", "true"}},
		{"cmd /c", []string{"cmd", "/c", "del /q *"}},
		{"cmd.exe", []string{"cmd.exe", "/c", "dir"}},
		{"command.com", []string{"command.com", "/c", "dir"}},
		{"powershell -Command", []string{"powershell", "-Command", "Remove-Item -Recurse ."}},
		{"pwsh -File", []string{"pwsh", "-File", "x.ps1"}},
		{"python -c", []string{"python", "-c", "import shutil"}},
		{"python3", []string{"python3", "x.py"}},
		{"python3.12", []string{"python3.12", "--version"}},
		{"python.exe", []string{"python.exe", "x.py"}},
		{"node -e", []string{"node", "-e", "require('fs').rmSync('.')"}},
		{"node.cmd", []string{"node.cmd", "x.js"}},
		{"deno", []string{"deno", "run", "x.ts"}},
		{"bun", []string{"bun", "x.ts"}},
		{"perl -e", []string{"perl", "-e", "unlink"}},
		{"perl5.36", []string{"perl5.36", "x.pl"}},
		{"ruby -e", []string{"ruby", "-e", "puts 1"}},
		{"php -r", []string{"php", "-r", "unlink('x');"}},
		{"lua5.4", []string{"lua5.4", "x.lua"}},
		{"awk system", []string{"awk", "BEGIN{system(\"rm x\")}"}},
		{"xargs rm", []string{"xargs", "rm"}},
		{"find -exec", []string{"find", ".", "-exec", "rm", "{}", ";"}},
		{"find -execdir", []string{"find", ".", "-execdir", "rm", "{}", ";"}},
		{"find -ok", []string{"find", ".", "-ok", "rm", "{}", ";"}},
		{"find -delete", []string{"find", ".", "-name", "*.o", "-delete"}},
		{"env cmd", []string{"env", "FOO=1", "rm", "x"}},
		{"env alone", []string{"env"}},
		{"nohup", []string{"nohup", "rm", "x"}},
		{"sudo", []string{"sudo", "ls"}},
		{"doas", []string{"doas", "ls"}},
		{"timeout", []string{"timeout", "5", "rm", "x"}},
		{"npx", []string{"npx", "some-package"}},
		{"npm exec", []string{"npm", "exec", "some-package"}},
		{"pnpm dlx", []string{"pnpm", "dlx", "some-package"}},
		{"wscript", []string{"wscript", "x.vbs"}},
		{"mshta", []string{"mshta", "x.hta"}},
		{"git -c", []string{"git", "-c", "core.hooksPath=/tmp/h", "status"}},
		{"git --config-env", []string{"git", "--config-env=core.sshCommand=X", "status"}},
		{"git branch -D", []string{"git", "branch", "-D", "main"}},
		{"git branch -d", []string{"git", "branch", "-d", "topic"}},
		{"git branch --delete", []string{"git", "branch", "--delete", "topic"}},
		{"git -C dir branch -D", []string{"git", "-C", "sub", "branch", "-D", "main"}},
		{"git stash drop", []string{"git", "stash", "drop"}},
		{"git stash clear", []string{"git", "stash", "clear"}},
		{"git checkout -- .", []string{"git", "checkout", "--", "."}},
		{"git checkout .", []string{"git", "checkout", "."}},
		{"git restore", []string{"git", "restore", "x.go"}},
		{"git switch --discard-changes", []string{"git", "switch", "--discard-changes", "main"}},
		{"git config", []string{"git", "config", "core.hooksPath", "h"}},
		{"git rm", []string{"git", "rm", "x.go"}},
		{"git reflog", []string{"git", "reflog", "expire", "--all"}},
		{"git tag -d", []string{"git", "tag", "-d", "v1"}},
		{"make clean", []string{"make", "clean"}},
		{"make distclean", []string{"make", "distclean"}},
		{"make install", []string{"make", "install"}},
		{"cargo clean", []string{"cargo", "clean"}},
		{"rm.cmd", []string{"rm.cmd", "x"}},
		{"rm.bat", []string{"rm.bat", "x"}},
		{"rm.ps1", []string{"rm.ps1", "x"}},
		{"del.com", []string{"del.com", "x"}},
		{"RM.CMD", []string{"RM.CMD", "x"}},
		{"scripts/sh", []string{"scripts/sh", "-c", "true"}},
		{"reg delete", []string{"reg", "delete", `HKCU\Software\X`}},
		{"schtasks /create", []string{"schtasks", "/create", "/tn", "x"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !Destructive(test.args) {
				t.Errorf("Destructive(%q) = false, want true: this ran unprompted under --yes", test.args)
			}
		})
	}
}

// The tables err towards yes, but not so far that ordinary work costs a
// question every time. These are the commands a model runs all day in agent
// mode and none of them changes anything it was not asked to.
func TestDestructiveLetsOrdinaryWorkThrough(t *testing.T) {
	tests := [][]string{
		{"go", "build", "./..."},
		{"go", "test", "./internal/shell"},
		{"go", "vet", "./..."},
		{"go", "run", "."},
		{"git", "status"},
		{"git", "add", "."},
		{"git", "log", "--", "x.go"},
		{"git", "diff", "--", "x.go"},
		{"git", "branch"},
		{"git", "branch", "-a"},
		{"git", "stash", "list"},
		{"git", "checkout", "main"},
		{"git", "switch", "main"},
		{"git", "tag"},
		{"git", "-C", "sub", "status"},
		{"make"},
		{"make", "test"},
		{"cargo", "build"},
		{"npm", "test"},
		{"ls", "-la"},
		{"cat", "x.go"},
		{"7z", "l", "x.7z"},
		{"bzip2", "-k", "x"},
		{"python-config", "--libs"},
		{"gofmt", "-l", "."},
	}
	for _, args := range tests {
		if Destructive(args) {
			t.Errorf("Destructive(%q) = true, want false: ordinary work should not cost a question", args)
		}
	}
}

// verb is where a Windows spelling is reduced to the name a table holds. An
// extension the operating system would launch without being told is dropped,
// and nothing else is, so that a filename with a dot in it survives.
func TestVerbStripsLaunchableExtensions(t *testing.T) {
	tests := []struct{ in, want string }{
		{"rm", "rm"},
		{"rm.exe", "rm"},
		{"rm.cmd", "rm"},
		{"rm.bat", "rm"},
		{"rm.ps1", "rm"},
		{"command.com", "command"},
		{"RM.EXE", "rm"},
		{`C:\tools\rm.cmd`, "rm"},
		{"./scripts/rm.bat", "rm"},
		{"python3.12", "python3.12"},
		{"tool.tar.gz", "tool.tar.gz"},
		{"  rm.exe  ", "rm"},
	}
	for _, test := range tests {
		if got := verb(test.in); got != test.want {
			t.Errorf("verb(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

// scrub is the whole of what keeps a configured key out of a child, so the
// rules are checked directly: configured and well-known names go, case does
// not save a name, and everything else is left exactly as it was.
func TestScrubDropsOnlyTheNamedVariables(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin",
		"GROQ_API_KEY=well-known",
		"Groq_Api_Key=well-known-in-another-case",
		"MY_PROVIDER_KEY=configured",
		"my_provider_key=configured-lowercase",
		"X_PROJECT_HEADER=configured-header",
		"HOME=/home/me",
		"=C:=C:\\odd-windows-entry",
		"NOEQUALS",
	}
	got := scrub(environ, []string{"MY_PROVIDER_KEY", " x_project_header ", ""})

	want := []string{"PATH=/usr/bin", "HOME=/home/me", "=C:=C:\\odd-windows-entry", "NOEQUALS"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("scrub() = %q, want %q", got, want)
	}
}

// TestEnvChild is the program run by TestRunHidesSecretsFromTheChild: when it
// finds itself started as a child it prints what it can see of the variables
// under test and exits, and otherwise does nothing. The test binary stands in
// for `env` so the test does not depend on a program the host may not have.
func TestEnvChild(t *testing.T) {
	if os.Getenv("YONDER_SHELL_CHILD") != "env" {
		return
	}
	for _, name := range []string{"YONDER_TEST_PLAIN", "YONDER_TEST_CONFIGURED_KEY", "GROQ_API_KEY"} {
		if value, ok := os.LookupEnv(name); ok {
			fmt.Printf("%s=%s\n", name, value)
		}
	}
	os.Exit(0)
}

// A child inherits the environment it needs to build things and not the key
// this program talks to its provider with. The configured name is passed in a
// different case from the one it was set in, because Windows would resolve it
// either way and the filter has to agree with the operating system about that.
func TestRunHidesSecretsFromTheChild(t *testing.T) {
	t.Setenv("YONDER_SHELL_CHILD", "env")
	t.Setenv("YONDER_TEST_PLAIN", "plain")
	t.Setenv("YONDER_TEST_CONFIGURED_KEY", "secret-configured")
	t.Setenv("GROQ_API_KEY", "secret-well-known")

	r := Open(filepath.Dir(os.Args[0]), perm.New(perm.Agent), "yonder_test_configured_key")
	result, err := r.Run(context.Background(), []string{"./" + filepath.Base(os.Args[0]), "-test.run=^TestEnvChild$"})
	if err != nil {
		t.Fatalf("running the child: %v", err)
	}
	if !strings.Contains(result.Output, "YONDER_TEST_PLAIN=plain") {
		t.Errorf("child output %q lacks the ordinary variable; the filter took too much", result.Output)
	}
	if strings.Contains(result.Output, "secret-") {
		t.Errorf("child output %q carries a secret; the filter took too little", result.Output)
	}
}

// A runner given nothing to hide still hides the well-known names, so a
// caller that has not been taught the parameter is not worse off than before.
func TestRunHidesWellKnownSecretsWithoutBeingAsked(t *testing.T) {
	t.Setenv("YONDER_SHELL_CHILD", "env")
	t.Setenv("GROQ_API_KEY", "secret-well-known")

	r := Open(filepath.Dir(os.Args[0]), perm.New(perm.Agent))
	result, err := r.Run(context.Background(), []string{"./" + filepath.Base(os.Args[0]), "-test.run=^TestEnvChild$"})
	if err != nil {
		t.Fatalf("running the child: %v", err)
	}
	if strings.Contains(result.Output, "GROQ_API_KEY") {
		t.Errorf("child output %q carries a well-known key", result.Output)
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
