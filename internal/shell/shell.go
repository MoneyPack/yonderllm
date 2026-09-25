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

	"github.com/MoneyPack/yonderllm/internal/perm"
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
	// hidden names environment variables a child must not inherit, over and
	// above wellKnownSecrets. Compared case-insensitively, because Windows
	// resolves names that way and a filter that respected case there would
	// let groq_api_key through while stripping GROQ_API_KEY.
	hidden []string
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
//
// hidden names environment variables that no child may inherit, typically
// the api_key_env and header_env variables the configuration resolved. It is
// optional: a caller with nothing to add still gets wellKnownSecrets removed,
// and one that has not been taught the parameter compiles unchanged.
func Open(dir string, policy perm.Policy, hidden ...string) *Runner {
	return &Runner{dir: dir, policy: policy, hidden: hidden}
}

// Current uses the working directory, which is the project the user started
// the program in and so the one they mean by an unqualified command. hidden
// is as for Open.
func Current(policy perm.Policy, hidden ...string) (*Runner, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("shell: locate working directory: %w", err)
	}
	return Open(dir, policy, hidden...), nil
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
	// The environment is inherited, less the credentials: a build that cannot
	// see PATH, HOME or a language's cache is not a useful build, but a child
	// has no business seeing the key this program talks to its provider
	// with. Whatever a command prints goes back to the model and into the
	// autosaved transcript, so `env` under auto-approval would otherwise be a
	// one-step exfiltration. Secrets the configuration never named can still
	// leak this way; that remains redaction's problem.
	cmd.Env = scrub(os.Environ(), r.hidden)

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

// interpreters are programs whose argument is itself a program: shells,
// language runtimes and launchers that hand the rest of the vector to
// something this package never sees. The destroyers table can only judge what
// is in the vector, and `sh -c "rm -rf ."` puts rm in a string rather than in
// argv, so every one of these is confirmed whatever follows it. `python
// --version` paying for that with one keystroke is the price of `python -c`
// not being a way round the table.
//
// Names are matched after trailing version digits are removed, so python3.12,
// perl5.36 and lua5.4 land on the same entries as their bare names.
var interpreters = map[string]bool{
	// Unix shells, and the BusyBox binary that contains one.
	"sh": true, "bash": true, "zsh": true, "dash": true, "fish": true,
	"ksh": true, "mksh": true, "ash": true, "csh": true, "tcsh": true,
	"busybox": true, "nu": true, "elvish": true, "xonsh": true,
	// Windows shells and script hosts. verb has already dropped .exe, .cmd,
	// .bat, .ps1 and .com, so command.com and cmd.exe arrive here bare.
	"cmd": true, "command": true, "powershell": true, "pwsh": true, "wt": true,
	"wscript": true, "cscript": true, "mshta": true, "rundll32": true,
	"regsvr32": true, "msiexec": true, "wmic": true, "runas": true, "start": true,
	// Language runtimes that take code on the command line or from a file.
	"python": true, "py": true, "pypy": true, "node": true, "nodejs": true,
	"deno": true, "bun": true, "perl": true, "ruby": true, "php": true,
	"lua": true, "luajit": true, "tclsh": true, "osascript": true,
	"awk": true, "gawk": true, "mawk": true, "nawk": true,
	// Launchers that run whatever they are given, or fetch it first.
	"xargs": true, "env": true, "nohup": true, "sudo": true, "doas": true,
	"su": true, "timeout": true, "nice": true, "ionice": true, "time": true,
	"watch": true, "setsid": true, "chroot": true, "strace": true, "ltrace": true,
	"npx": true, "bunx": true, "uvx": true, "pipx": true,
}

// subcommands are programs that are harmless until told what to do, paired
// with the verbs that make them irreversible. Publishing is here alongside
// deletion: a released version cannot be recalled, and a force-push discards
// history that was somebody else's. Verbs that run arbitrary code count too,
// because `npm exec` is an interpreter that happens to be spelled as two
// words.
var subcommands = map[string]map[string]bool{
	"git": {
		"push": true, "reset": true, "clean": true, "rebase": true, "gc": true,
		"prune": true, "restore": true, "rm": true, "mv": true, "config": true,
		"filter-branch": true, "filter-repo": true, "update-ref": true,
		"reflog": true, "worktree": true, "submodule": true,
	},
	"npm":       {"publish": true, "unpublish": true, "exec": true},
	"pnpm":      {"publish": true, "exec": true, "dlx": true},
	"yarn":      {"publish": true, "exec": true, "dlx": true},
	"bun":       {"x": true},
	"cargo":     {"publish": true, "yank": true, "clean": true},
	"docker":    {"rm": true, "rmi": true, "prune": true, "kill": true, "system": true},
	"kubectl":   {"delete": true, "drain": true, "apply": true, "replace": true},
	"terraform": {"apply": true, "destroy": true},
	"gh":        {"release": true, "repo": true},
	"go":        {"clean": true},
	"make":      {"clean": true, "distclean": true, "mrproper": true, "install": true, "uninstall": true},
	"pip":       {"uninstall": true},
	"brew":      {"uninstall": true, "remove": true},
	"apt":       {"remove": true, "purge": true},
	"systemctl": {"stop": true, "disable": true, "mask": true},
	"reg":       {"add": true, "delete": true, "import": true, "restore": true},
	"schtasks":  {"/create": true, "/delete": true, "/run": true, "/change": true},
	"sc":        {"create": true, "delete": true, "config": true, "stop": true},
}

// gitVerbArgs are the arguments that make an otherwise safe git verb discard
// work: `git branch -D`, `git stash drop`, `git checkout -- .`. They are keyed
// by verb because the same spelling is harmless elsewhere — `git add .` and
// `git log -- file` should not cost a question — and looked for anywhere after
// the verb, because git accepts options in either order.
var gitVerbArgs = map[string]map[string]bool{
	"branch":   {"-D": true, "-d": true, "-M": true, "-m": true},
	"stash":    {"drop": true, "clear": true},
	"checkout": {"--": true, ".": true, "-B": true},
	"switch":   {"--discard-changes": true, "-C": true},
	"tag":      {"-d": true},
	"remote":   {"remove": true, "rm": true, "prune": true},
}

// Destructive reports whether a vector should be confirmed even where a mode
// was configured to approve commands automatically.
//
// The answer errs towards yes. Over-reporting costs one question that the user
// answers in a keystroke; under-reporting costs whatever the command deleted.
// So a program in any table counts, and so does a force flag on anything at
// all, because --force is precisely how an otherwise cautious command is told
// to stop refusing.
//
// This is a table of names, not an understanding of what a command does. A
// program the tables have never heard of is not destructive as far as this
// function can tell, and a caller relying on it under auto-approval is relying
// on exactly that: recognised commands are confirmed, unrecognised ones run.
func Destructive(args []string) bool {
	if len(args) == 0 {
		return false
	}
	name := verb(args[0])
	if destroyers[name] || interpreters[unversioned(name)] {
		return true
	}
	if verbs := subcommands[name]; verbs != nil {
		for _, arg := range args[1:] {
			if verbs[arg] {
				return true
			}
		}
	}
	switch name {
	case "git":
		if gitDestructive(args[1:]) {
			return true
		}
	case "find":
		if findExecutes(args[1:]) {
			return true
		}
	}
	return forced(args[1:])
}

// gitDestructive catches the git invocations that hide behind a safe verb.
//
// `-c key=value` sets any configuration key for one command, including
// core.hooksPath and core.sshCommand, which turns `git -c ... status` into a
// way to run a program of the caller's choosing; --config-env is the same door
// with the value read from the environment. The verb is the first argument not
// spelled as an option, and its arguments are checked against the ones that
// throw work away: a branch deleted, a stash dropped, a checkout of `--` or
// `.` that overwrites the working tree.
func gitDestructive(args []string) bool {
	var verbArgs map[string]bool
	skip := false
	for _, arg := range args {
		if skip {
			skip = false
			continue
		}
		if arg == "-c" || strings.HasPrefix(arg, "--config-env") {
			return true
		}
		if verbArgs == nil {
			// Global options that take their value as the next argument,
			// which must not be mistaken for the verb.
			if arg == "-C" || arg == "--git-dir" || arg == "--work-tree" {
				skip = true
				continue
			}
			if !strings.HasPrefix(arg, "-") {
				verbArgs = gitVerbArgs[arg]
				if verbArgs == nil {
					// A verb with nothing to catch: subcommands and
					// forced have already had their say.
					return false
				}
			}
			continue
		}
		if verbArgs[arg] {
			return true
		}
	}
	return false
}

// findExecutes reports a find that runs a program for each result, which
// makes it an interpreter with a search bolted on. -delete is caught by
// forced already.
func findExecutes(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-exec", "-execdir", "-ok", "-okdir":
			return true
		}
	}
	return false
}

// verb reduces a program to the name a table can match, so that ./scripts/rm
// and /usr/bin/rm are not read as something new. Windows lets a program be
// named with or without its extension, and a batch or PowerShell wrapper named
// rm.cmd is still rm to whoever wrote it, so every launchable extension is
// removed and not only .exe.
func verb(arg string) string {
	name := strings.ToLower(filepath.Base(filepath.ToSlash(strings.TrimSpace(arg))))
	for _, ext := range []string{".exe", ".cmd", ".bat", ".ps1", ".com"} {
		if strings.HasSuffix(name, ext) {
			return strings.TrimSuffix(name, ext)
		}
	}
	return name
}

// unversioned drops the trailing version an interpreter is often installed
// under: python3.12, perl5.36, lua5.4. It is applied only to the interpreters
// lookup, where the names in question live; 7z and bzip2 are not affected
// because neither is in that table.
func unversioned(name string) string {
	return strings.TrimRight(name, "0123456789.")
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
// separator in it is resolved against the project, and a name that spells out
// a climb above it is refused.
//
// This is a check on how the name is spelled, not containment of what runs.
// A bare name executes whatever PATH resolves it to, anywhere on the machine;
// a relative path may be a symlink whose target is outside the project; and
// the program, once running, is an ordinary process with the user's whole
// account at its disposal. What the check buys is that a vector cannot name a
// place outside the tree in so many words and have that be the reason it was
// approved. The workspace's os.Root gives file access a real boundary; nothing
// here gives execution one, and docs/SAFETY.md says so.
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

// wellKnownSecrets are variables that hold a credential wherever they appear,
// stripped from every child regardless of what the configuration named. The
// list is short on purpose: it covers the providers this program ships
// defaults for and the tokens a developer's shell most often carries, and the
// configured names cover the rest. It is not an attempt to enumerate every
// secret a machine might hold, which is a job no list can do.
var wellKnownSecrets = []string{
	"GROQ_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENROUTER_API_KEY",
	"SURPLUS_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "MISTRAL_API_KEY",
	"COHERE_API_KEY", "DEEPSEEK_API_KEY", "XAI_API_KEY", "TOGETHER_API_KEY",
	"AZURE_OPENAI_API_KEY", "HF_TOKEN", "HUGGING_FACE_HUB_TOKEN",
	"GITHUB_TOKEN", "GH_TOKEN", "NPM_TOKEN", "AWS_SECRET_ACCESS_KEY",
	"AWS_SESSION_TOKEN",
}

// scrub returns environ without the named variables. Names are compared
// without regard to case, as Windows does; a filter that let `Groq_Api_Key`
// through because the configuration spelled it in capitals would have failed
// at exactly the thing it exists for. An entry with no '=' is kept as it is,
// since it is not a variable this filter could be asked about.
func scrub(environ []string, hidden []string) []string {
	drop := make(map[string]bool, len(hidden)+len(wellKnownSecrets))
	for _, name := range wellKnownSecrets {
		drop[strings.ToUpper(name)] = true
	}
	for _, name := range hidden {
		if name = strings.TrimSpace(name); name != "" {
			drop[strings.ToUpper(name)] = true
		}
	}

	kept := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, found := strings.Cut(entry, "=")
		if found && drop[strings.ToUpper(name)] {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
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
