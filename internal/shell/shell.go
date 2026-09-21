// Package shell is the only way this program starts another one. Like
// workspace, every entry point takes a perm.Policy and asks it first, so a
// mode that forbids execution has no path around this package to a process.
//
// A command is an argument vector, never a string for a shell to interpret.
// The vector is what the operating system actually receives, so it is also the
// only honest thing to show someone who is being asked to approve a command:
// a string that a shell would still expand, split and redirect can run
// something other than what the reader agreed to. It follows that quoting,
// pipes, globs and redirection do not work here, which is a limit worth
// having rather than a feature to add back.
//
// What the policy cannot settle here is a command a mode permits only after
// the user approves it, and a destructive command that must be confirmed even
// where approval was configured away. This package holds a directory and a
// policy, not a terminal, so it enforces the absolute half — refusing what the
// mode forbids outright — names which commands are destructive, and leaves the
// asking to the caller that owns an interface.
package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"yonderllm/internal/perm"
)

const (
	// maxOutputBytes caps what one command may hand back. A command that
	// prints without end must not be able to exhaust memory before its
	// timeout arrives, and a caller that wants less will clip further.
	maxOutputBytes = 1 << 20
	// timeout is how long a command may run. A model cannot notice that it
	// started a server and is now waiting forever, so the limit is imposed
	// rather than offered: anything long-running is the user's to start in
	// their own terminal.
	timeout = 30 * time.Second
)

// Runner starts commands in one directory under one policy.
type Runner struct {
	dir    string
	policy perm.Policy
}

// Result is what running a command produced.
type Result struct {
	// Output is stdout and stderr interleaved, in the order the command
	// wrote them, because a failing command explains itself across both
	// and separating them loses which message answered which.
	Output string
	// Dropped counts bytes past maxOutputBytes that were discarded.
	Dropped int
	// Code is the exit status. A command killed by a signal reports the
	// negative value os/exec gives it.
	Code int
	// TimedOut reports that the command was killed for exceeding timeout,
	// which is not the same as a command that chose to fail.
	TimedOut bool
}

// Open prepares dir for running commands under policy. The policy is not
// consulted here, for the same reason workspace.Open does not consult it:
// naming a directory is not one of the actions a mode governs.
func Open(dir string, policy perm.Policy) *Runner {
	return &Runner{dir: dir, policy: policy}
}

// Current uses the working directory, which is the project the user started
// the program in and so the one they mean by an unqualified command.
func Current(policy perm.Policy) (*Runner, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("shell: locate working directory: %w", err)
	}
	return Open(dir, policy), nil
}

// Dir reports where commands will run, for display.
func (r *Runner) Dir() string { return r.dir }

// Run executes args and waits for it to finish.
//
// The vector is passed to the operating system as given: args[0] is the
// program and the rest are its arguments, one element each, with no splitting
// or expansion in between. A command that exits non-zero is a result and not
// an error — a failing test suite is exactly the answer the caller wanted —
// whereas a program that could not be started at all is an error, because
// nothing ran and there is no exit status to report.
func (r *Runner) Run(ctx context.Context, args []string) (Result, error) {
	if err := r.authorize(perm.Exec); err != nil {
		return Result{}, err
	}
	name, err := program(args)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	// The timeout hangs off the caller's context rather than replacing it,
	// so a reader who interrupts is obeyed at once and does not wait out a
	// limit that exists for a different reason.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := &capped{}
	cmd := exec.CommandContext(ctx, name, args[1:]...)
	// Descendants may inherit output pipes after the direct child exits or is
	// killed. Bound the wait for EOF rather than keeping the session stuck.
	cmd.WaitDelay = time.Second
	cmd.Dir = r.dir
	// Nothing is on stdin. A command that prompts should see a closed input
	// and give up, rather than block until the timeout kills it while the
	// model waits for output that will never come.
	cmd.Stdin = nil
	// One writer for both streams, which os/exec answers with a single pipe,
	// so the interleaving is the command's own and no goroutine races for it.
	cmd.Stdout = out
	cmd.Stderr = out
	// The environment is inherited: a build that cannot see PATH, HOME or a
	// language's cache is not a useful build. That does mean a command can
	// print a secret it was given, which is redaction's problem and not
	// something this package can tell apart from ordinary output.
	cmd.Env = os.Environ()

	runErr := cmd.Run()
	if errors.Is(runErr, exec.ErrWaitDelay) {
		return Result{}, fmt.Errorf("shell: output pipes remained open after the command exited: %w", runErr)
	}

	// A command that never started leaves no process state behind, so there
	// is no exit status to report and nothing to hand back as a result. That
	// is the one case this package calls an error: a missing program is the
	// caller's mistake, not an answer from a program that ran.
	if cmd.ProcessState == nil {
		if err := parentErr(ctx); err != nil {
			return Result{}, err
		}
		return Result{}, fmt.Errorf("shell: run %s: %w", name, runErr)
	}

	result := Result{
		Output:  out.text(),
		Dropped: out.over,
		Code:    cmd.ProcessState.ExitCode(),
	}

	// The kill arrives as an ordinary failure, so the context is what says
	// why. A parent that was cancelled is reported as cancellation rather
	// than as a command that misbehaved.
	switch {
	case errors.Is(context.Cause(ctx), context.DeadlineExceeded):
		result.TimedOut = true
		return result, nil
	case runErr == nil:
		return result, nil
	}
	if err := parentErr(ctx); err != nil {
		return Result{}, err
	}

	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		return result, nil
	}
	return Result{}, fmt.Errorf("shell: run %s: %w", name, runErr)
}

// authorize resolves an action against the policy. Only Deny is refused, for
// the reason workspace gives at the same seam: a mode that answers Ask has not
// said no, it has asked a question this package has nobody to put. The caller
// that owns an interface settles it before calling, and withholds the
// capability altogether when it has nobody to ask.
func (r *Runner) authorize(a perm.Action) error {
	if r.policy.Check(a) == perm.Deny {
		return &perm.DeniedError{Mode: r.policy.Mode(), Action: a}
	}
	return nil
}

// parentErr reports the caller's own cancellation, ignoring the timeout this
// package added.
func parentErr(ctx context.Context) error {
	if errors.Is(context.Cause(ctx), context.Canceled) {
		return context.Canceled
	}
	return nil
}

// destroyers are programs whose ordinary use is to remove or overwrite
// something. There is no safe invocation to carve out here: a caller reaching
// for rm has already decided that something should stop existing.
var destroyers = map[string]bool{
	"rm": true, "rmdir": true, "del": true, "erase": true, "rd": true,
	"mv": true, "move": true, "dd": true, "shred": true, "truncate": true,
	"mkfs": true, "fdisk": true, "format": true, "diskpart": true,
	"shutdown": true, "reboot": true, "halt": true, "poweroff": true,
	"kill": true, "killall": true, "pkill": true, "taskkill": true,
	"chown": true, "chmod": true, "sudo": true, "doas": true, "su": true,
}

// subcommands are programs that are harmless until told what to do, paired
// with the verbs that make them irreversible. Publishing is here alongside
// deletion: a released version cannot be recalled, and a force-push discards
// history that was somebody else's.
var subcommands = map[string]map[string]bool{
	"git":       {"push": true, "reset": true, "clean": true, "rebase": true, "gc": true, "prune": true},
	"npm":       {"publish": true, "unpublish": true},
	"pnpm":      {"publish": true},
	"yarn":      {"publish": true},
	"cargo":     {"publish": true, "yank": true},
	"docker":    {"rm": true, "rmi": true, "prune": true, "kill": true, "system": true},
	"kubectl":   {"delete": true, "drain": true, "apply": true, "replace": true},
	"terraform": {"apply": true, "destroy": true},
	"gh":        {"release": true, "repo": true},
	"go":        {"clean": true},
	"pip":       {"uninstall": true},
	"brew":      {"uninstall": true, "remove": true},
	"apt":       {"remove": true, "purge": true},
	"systemctl": {"stop": true, "disable": true, "mask": true},
}

// Destructive reports whether a vector should be confirmed even where a mode
// was configured to approve commands automatically.
//
// The answer errs towards yes. Over-reporting costs one question that the user
// answers in a keystroke; under-reporting costs whatever the command deleted.
// So a program in either table counts, and so does a force flag on anything at
// all, because --force is precisely how an otherwise cautious command is told
// to stop refusing.
func Destructive(args []string) bool {
	if len(args) == 0 {
		return false
	}
	name := verb(args[0])
	if destroyers[name] {
		return true
	}
	if verbs := subcommands[name]; verbs != nil {
		for _, arg := range args[1:] {
			if verbs[arg] {
				return true
			}
		}
	}
	return forced(args[1:])
}

// verb reduces a program to the name a table can match, so that ./scripts/rm
// and /usr/bin/rm are not read as something new.
func verb(arg string) string {
	name := filepath.Base(filepath.ToSlash(strings.TrimSpace(arg)))
	return strings.TrimSuffix(strings.ToLower(name), ".exe")
}

// forced reports a flag that overrides a program's own caution.
func forced(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-f", "--force", "-force", "/f", "--hard", "--delete", "-delete", "--no-preserve-root":
			return true
		}
		// A bundle like -rf is one argument to the shell but several flags
		// to the program, so the letters are read individually rather than
		// compared whole.
		if len(arg) > 1 && arg[0] == '-' && arg[1] != '-' && strings.ContainsRune(arg[1:], 'f') {
			return true
		}
	}
	return false
}

// program checks the vector and returns the name to execute.
//
// A rooted name is refused first, because C:tool.exe carries no separator and
// would otherwise pass for a bare name. What remains without a separator is
// looked up on PATH, which is how a person would type it. A name with a
// separator in it is a file in the project, and is held to the workspace's
// rule: a command may not be fetched from outside the tree the user opened,
// whether it says so plainly or arrives as a climb through it.
func program(args []string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("shell: no command named")
	}
	name := strings.TrimSpace(args[0])
	if name == "" {
		return "", errors.New("shell: no command named")
	}
	if rooted(name) {
		return "", fmt.Errorf("shell: %s is outside the workspace", name)
	}
	if !strings.ContainsAny(name, `/\`) {
		return name, nil
	}
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("shell: %s is outside the workspace", name)
	}
	// Kept relative and paired with cmd.Dir, so the program is resolved
	// against the project rather than against wherever the process happens
	// to be by the time it starts.
	return "." + string(filepath.Separator) + cleaned, nil
}

// rooted reports whether a name reaches for a fixed place on the machine
// rather than a place inside the project.
//
// filepath.IsAbs answers only for the host it was compiled for: a Windows
// build reads /usr/bin/rm as relative and a Unix build reads C:\Windows the
// same way. A vector arrives here as text a model wrote, so both spellings are
// refused on both hosts — the alternative is a name the check waves through
// and the operating system then resolves somewhere else entirely.
func rooted(name string) bool {
	if filepath.IsAbs(name) {
		return true
	}
	// A leading separator, in either spelling: /etc/passwd, and the UNC and
	// drive-relative forms \\host\share and \Windows.
	if name[0] == '/' || name[0] == '\\' {
		return true
	}
	// A drive letter, as in C:\Windows or the drive-relative C:notes.txt.
	// The second names a place on that drive, not a file in the project, and
	// carries no separator to give itself away.
	if len(name) >= 2 && name[1] == ':' {
		c := name[0]
		return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
	}
	return false
}

// capped collects output up to maxOutputBytes and counts what it discarded.
type capped struct {
	buf  []byte
	over int
}

// Write never reports a short write. Telling the command its output failed
// would make a truncated result look like a broken pipe, and the point of the
// cap is to keep reading cheaply rather than to stop the command.
func (c *capped) Write(p []byte) (int, error) {
	room := maxOutputBytes - len(c.buf)
	switch {
	case room <= 0:
		c.over += len(p)
	case len(p) > room:
		c.buf = append(c.buf, p[:room]...)
		c.over += len(p) - room
	default:
		c.buf = append(c.buf, p...)
	}
	return len(p), nil
}

func (c *capped) text() string { return string(c.buf) }
