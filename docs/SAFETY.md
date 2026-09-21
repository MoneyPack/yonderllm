# Safety and data

Chat mode exposes no filesystem/shell tools. Code mode permits reads/searches and
asks before writes. Agent mode can execute commands, subject to consent.
Destructive operations require confirmation even with `--yes`.

`/mode` changes the actual tool set while idle. Switching modes does not remove
already-read data from conversation context. `/clear` starts fresh context.

Workspace path confinement applies to built-in file tools. Shell execution runs
with your OS user permissions; it is not an operating-system sandbox.

Keys are read from environment variables. Config stores variable names rather
than credentials. Provider errors redact the exact configured key and recognized
key-like values. Redaction is best effort; avoid sending credentials in prompts.

Saved conversations are unencrypted, versioned files under the user config
directory. Recognized secrets are redacted before saving, but private prose and
tool results may remain. `--no-save` disables autosave; explicit `/save` still
saves. Resume restores context and model choice, never approval or permission state.

Daily usage is local bookkeeping, not a provider billing guarantee. It is shared
between processes, and storage errors prevent dispatch. Clearing cache resets it.

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
