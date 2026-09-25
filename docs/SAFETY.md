# Safety and data

Chat mode exposes no filesystem/shell tools. Code mode permits reads/searches and
asks before writes. Agent mode can execute commands, subject to consent.

## Modes

`--mode` (or `mode` in the config file, or `YONDERLLM_MODE`) sets how much
yonderllm is permitted to touch. Sessions start in `chat`; nothing promotes
itself out of it.

| Mode | Read / search | Write | Execute |
| --- | --- | --- | --- |
| `chat` *(default)* | ✗ | ✗ | ✗ |
| `code` | allow | ask | ✗ |
| `agent` | allow | ask | ask |

The policy is enforced in one place, `internal/perm`, so the rules can be read
and tested as a unit rather than inferred from call sites. Reading and searching
are live in both directions: you reach them with `/read` and `/search`, the
model reaches them as tools. Both go through the same `perm.Policy`, so a mode
that refuses you refuses the model too.

`--yes` turns agent mode's two `ask` cells into `allow`, with the exceptions
listed under [what `--yes` does and does not waive](#what---yes-does-and-does-not-waive).
It is refused outside agent mode.

## Tools

`/read` and `/search` are you looking at the project. Tools are the *model*
looking at it. In `code` and `agent` mode yonderllm tells the model which
capabilities it has, and when the model asks for one, yonderllm runs it and
feeds the result back without leaving the turn you are already in.

| Tool | Argument(s) | What the model gets |
| --- | --- | --- |
| `read_file` | `path` | One UTF-8 text file, addressed relative to the project root |
| `search_files` | `query` | Every line in the project matching a literal, case-insensitive string, with its path |
| `write_file` | `path`, `content` | Creates or replaces one UTF-8 text file; missing parent directories are created |
| `run_command` | `command` (array) | Runs one program as an argument vector and returns its combined output and exit status |

Arguments are described to the model by JSON Schema, so a malformed call is
refused with a sentence the model can act on rather than silently mishandled:

```json
{"name": "read_file",    "arguments": {"path":  "internal/cli/run.go"}}
{"name": "search_files", "arguments": {"query": "func newSessionFor"}}
{"name": "write_file",   "arguments": {"path": "notes.md", "content": "# Notes\n"}}
{"name": "run_command",  "arguments": {"command": ["go", "test", "./internal/perm"]}}
```

Search is literal, not a regular expression: a model that guesses at a regex
dialect wastes a round trip finding out which one it got. `run_command` takes a
vector, not a shell line: there is no shell, so pipes, redirection, globs and
variable expansion do not work, and the vector the model wrote is exactly the
vector that runs and exactly what you are shown when asked to approve it.

### What the model is told

The tool list is built from the permission policy, not filtered after the fact.
A capability the current mode does not allow is never described to the model,
so a refusal is not something the model has to be talked out of.

| Mode | Tools offered |
| --- | --- |
| `chat` | none |
| `code` | `read_file`, `search_files`, `write_file` (approval required) |
| `agent` | `read_file`, `search_files`, `write_file`, `run_command` (approval required unless `--yes`) |

A tool the mode would *ask* about is offered only when something can put the
question to you. In the TUI that is the approval prompt (`y` allows, anything
else denies). In a non-interactive run — `run --json`, a pipe, anything without
a terminal — there is no approver, so `write_file` and `run_command` are
withheld from the model entirely rather than offered and then refused. A script
that wants them must ask explicitly with `--mode agent --yes`, and even then the
actions listed below are refused because nobody is there to confirm them.

### Approval

The prompt states the action, its target, and enough of the change to judge
it: a diff for a write (the file is read before the question so the diff is
against what is actually on disk), the exact argument vector for a command.

- Deny is the default. An empty answer, any key other than `y`, an interrupt,
  a closed input, or a cancelled context all mean no.
- A denial is not an error. It goes back to the model as a tool result saying
  you refused, so it can propose something else instead of retrying blindly.
- Approval is per call, never remembered. Approving one write does not approve
  the next.
- An open approval question keeps control of the keyboard; `/mode` and new
  prompts wait until it is answered. If the exchange ends (timeout, cancel)
  while a question is open, it is resolved as denied.

### The loop

A turn is a conversation, not a single request. The model may answer, or it
may call tools; if it calls, yonderllm runs them in order, appends each result,
and asks again. The loop is bounded at **six rounds**. On the last round the
tools are withheld, which turns the ceiling into a prose answer instead of a
truncation. The whole exchange uses one reservation against the daily cap, and
token usage accumulates across the turn so the footer reports the true cost.

Tool results are capped at 8 KiB and clipped on a line boundary, with a note
saying how many lines were dropped. A result goes straight into the next
prompt, so an unbounded one would spend your context window on a file the
model only needed to glance at.

### What you see

Tool calls are not hidden. Each one lands in the transcript as its own block:
the tool's name, the arguments it was called with, and the result once it
returns, clipped for display.

```
tool search_files
{"query": "parseFlags"}
  3 matches for "parseFlags"
  cmd/app/main.go:24: flags, err := parseFlags(os.Args[1:])
  internal/app/flags.go:31: // parseFlags reads an argument list into a Flags.
  internal/app/flags.go:36: func parseFlags(args []string) (Flags, error) {
```

A finished command is shown the same way: the vector as it ran, its exit
status (stated even when zero, so silence cannot be mistaken for failure) or a
note that it timed out, then its output. An answer that leans on a file or a
command you did not expect is visible as it happens.

Scripts see the same thing: `run --json` reports each call as a `tool` event
and each outcome as a `tool_result`, described in [scripting](SCRIPTING.md).
Plain `run` and `ask` print only the answer.

## Containment

Every read, search and write goes through `internal/workspace`, which holds an
`os.Root` on the directory yonderllm was started in. Containment is a property
of the handle, not a string check performed hopefully at the top of a function.

| Refused | Because |
| --- | --- |
| Absolute paths | Addressable only inside the project |
| Anything reaching `..` past the root | Escape, rejected before any syscall |
| Symlinks that resolve outside the root | `os.Root` refuses them |
| Windows device names such as `NUL`, `COM1` | Not files, however they resolve |
| Binary files | Sniffed, not trusted by extension |
| Files over 1 MiB, or writes over 1 MiB | A prompt is not a place to put a megabyte |
| Anything under `.git/`, `node_modules/`, `vendor/`, `bin/`, `dist/`, `build/`, `target/`, `__pycache__/`, `.hg/`, `.svn/` | Never the model's to read; writes under `.git/` are refused outright |

Searches stop at 200 matches and skip the same directories. Unreadable files
are skipped rather than aborting the walk: one permission error in a tree
should not cost you the other 900 results.

Commands must name a program on `PATH` or inside the project, run with the
project as working directory, are stopped after 30 seconds, and have their
combined output capped at 1 MiB (the number of dropped bytes is reported). The
checks on the program name are lexical; see [files and processes](#files-and-processes)
for what containment does *not* cover.

## What `--yes` does and does not waive

`--yes` answers write and exec approvals in advance. It does not waive the
confirmation of a *recognised* destructive or interpreter command, a write to a
hook-like file, or a read of a credential-like file. "Recognised" means matched
by a table of names in `internal/shell` and `internal/workspace`, not understood
semantically:

- Commands: programs whose ordinary use deletes or overwrites (`rm`, `dd`,
  `git push`, `git reset`, `kubectl delete`, ...), any `-f`/`--force`-style
  flag, and every shell, script interpreter or launcher (`sh`, `bash`, `cmd`,
  `powershell`, `python*`, `node`, `perl`, `ruby`, `xargs`, `env`, `nohup`,
  `sudo`, `find -exec`, `git -c`, `git branch -D`, `git stash drop`,
  `git checkout -- .`, `make clean`, ...). Windows launchable extensions
  (`.exe`, `.cmd`, `.bat`, `.ps1`, `.com`) are stripped before matching, so
  `rm.cmd` is `rm`. A command the tables do not recognise runs unprompted
  under `--yes`.
- Writes: anything under a dot-directory (`.githooks/`, `.github/`,
  `.vscode/`, ...), any dotfile (`.envrc`, `.bashrc`, `.gitignore`), and
  build, manifest and tool-configuration files (`Makefile`, `*.mk`,
  `package.json`, `go.mod`, `go.sum`, `Cargo.toml`, `pyproject.toml`,
  `*.ps1`, `docker-compose*.yml`, ...). Writes under `.git/` are refused
  outright in every mode; no approval enables them.
- Reads: files named like credentials (`.env*`, `id_rsa*`, `*.pem`, `*.key`,
  `.netrc`, `.npmrc`, `.aws/credentials`, `.ssh/*`, `secrets.*`, ...) are
  refused in code mode and confirmed in agent mode, including under `--yes`.
  Reads under `.git/` and the other directories search skips (`node_modules`,
  `vendor`, `bin`, `dist`, `build`, `target`, ...) are refused.

`/mode` changes the actual tool set while idle. The switched-to mode is built
from the mode name alone, so `--yes` is dropped: after `/mode agent`, writes and
commands prompt as if the flag had not been given, until the program is
restarted with it. Switching modes does not remove already-read data from
conversation context. `/clear` starts fresh context.

Workspace path confinement applies to built-in file tools. Shell execution runs
with your OS user permissions; it is not an operating-system sandbox. The check
on a command's program name is lexical only: a name spelled as an absolute path
or a climb out of the project is refused, but a bare name runs whatever `PATH`
resolves it to and a relative path may be a symlink to anywhere. Nothing
confines what an approved program then does.

Commands do not inherit the environment variables the configuration names as
credentials (`api_key_env`, `header_env`) or a short list of well-known key
names (`OPENAI_API_KEY`, `GITHUB_TOKEN`, `AWS_SECRET_ACCESS_KEY`, ...). Names
are matched case-insensitively. Other secrets in your environment are still
inherited and can be printed by a command; that output goes to the provider and
into the saved conversation.

## Saved conversations

Interactive sessions automatically save after each completed exchange. Resume
the most recent one with `yonderllm --last`, or keep a named snapshot with
`/save <name>` in the TUI and reopen it with `--resume <name>`.

```sh
yonderllm sessions                      # list snapshots, newest first
yonderllm run --resume project-notes "What should I change first?"
yonderllm ask --save project-notes "Explain this project"
yonderllm --last
yonderllm --no-save
yonderllm sessions delete project-notes
```

`ask` and headless `run` start fresh without saving unless you use `--save`,
`--resume`, or `--last`. `--no-save` disables automatic saving; an explicit
`/save` still writes a snapshot. Names use lowercase letters, digits, dots,
hyphens and underscores (up to 80 characters, no leading/trailing dot). Saving
an existing name replaces that snapshot.

Resume restores messages, the system prompt, and provider/model selection.
Explicit `--provider` and `--model` flags override the saved selection.
Credentials, permission modes, approvals, and usage counters come from the
current run; saved tool calls are historical context and are never executed on
load. Each resumed run gets a new autosave name unless `--save <name>` is
supplied. `/clear` starts a new autosave without deleting the prior snapshot.

Files are versioned JSON under `yonderllm/sessions` in the OS user
configuration directory (`%APPDATA%` on Windows, `$XDG_CONFIG_HOME` or
`~/.config` on Linux, `~/Library/Application Support` on macOS). Saves use a
filesystem lock and temporary-file replacement. `sessions` lists valid
snapshots newest first; unsupported or corrupt files are excluded from listing
and rejected on explicit resume. Snapshots are limited to 16 MiB.

Recognized credential patterns are redacted from message text, system prompts,
and tool arguments before saving. Files are **not encrypted**, and pattern-based
redaction cannot identify every secret; private prose and tool results may
remain. Use `--no-save` for sensitive sessions. On Unix, newly created files use
owner-only permissions; Windows access follows the user directory's ACLs.
Autosave preserves completed exchanges, not a response interrupted mid-stream.
Named snapshots include conversation context, not UI-only notices or the output
of local `/read` and `/search` commands. Resume restores context and model
choice, never approval or permission state.

## The daily cap

`daily_cap` is a local counter, not a provider feature. yonderllm tracks how
many requests it has made today and refuses the one that would exceed the cap:

```
daily request cap reached (200/200), resets at 00:00
```

It exists because free tiers punish enthusiasm quietly and cheap tiers bill it.
`/usage` in the TUI shows where you stand.

The request count is shared across CLI invocations and TUI sessions on this
machine and resets at local midnight. It is stored in `yonderllm/usage.json`
under the OS user cache directory (`%LOCALAPPDATA%` on Windows,
`$XDG_CACHE_HOME` or `~/.cache` on Linux, `~/Library/Caches` on macOS).
Concurrent processes lock the counter before reserving a request. The count is
saved before dispatch, so a process crash can leave a reservation charged. One
exchange, including fallback attempts and tool rounds, uses one reservation.
Token totals and provider breakdowns remain per session.

If the counter cannot be read, locked, or saved, the request fails before
contacting a provider rather than silently resetting the allowance. Fix the
reported path or permissions; removing `usage.json` deliberately resets the
count, and so does clearing the OS cache. This is a local request limit, not a
provider quota or a guaranteed spending limit. Different processes use their
own configured cap against the same count; `--daily-cap 0` (or `daily_cap = 0`)
disables the limit but still records requests.

## Transport and configuration

Keys are read from environment variables. Config stores variable names rather
than credentials, so the file is safe to commit or sync. Provider errors redact
the exact configured key and recognized key-like values. Redaction is best
effort; avoid sending credentials in prompts.

Provider endpoints must be HTTP(S) without embedded URL credentials. Prefer HTTPS
for remote hosts; HTTP remains available for trusted local servers. Redirects are
not followed. On Unix config files must be owner-only; Windows relies on ACLs.

## Terminal output

TUI transcript text, labels, and approval targets render terminal control bytes
as visible escapes (for example `\x1b`), including C1 and bidirectional control
characters. Application-owned colors and cursor handling remain active. Plain
CLI output is escaped when stdout is a terminal; stderr is always escaped.
JSON output preserves original string values, and piped plain stdout remains
raw data: consumers displaying it in a terminal must apply their own escaping.
This prevents control-sequence execution, not all misleading natural-language
content or visual impersonation. Read the actual action before approving it.

## Files and processes

Built-in reads and searches cap bytes consumed, including when a regular file
grows after its size check. Known special files are refused by direct reads.
Opening a path can still race replacement by a special file; portable protection
against that race is not claimed. Writes recheck cancellation and the displayed
content snapshot after approval; this detects ordinary concurrent edits but is not an atomic
compare-and-swap or a defense against a hostile same-user process.

Writes require a readable bounded text snapshot (or a confirmed missing file)
before approval. Files that cannot be snapshotted must be handled manually.
Affirmative approval is disabled in undersized terminals that hide request
details; enlarge the terminal to approve, or deny the request.

Commands have a one-second output-drain grace period after direct-child exit or
cancellation, preventing inherited pipes from holding the session indefinitely.
This does not kill all descendants: approved programs can spawn surviving
processes. Run untrusted commands in an OS sandbox if containment is required.
