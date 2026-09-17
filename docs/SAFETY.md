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
