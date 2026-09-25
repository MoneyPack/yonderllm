# yonderllm

[![CI](https://github.com/MoneyPack/yonderllm/actions/workflows/ci.yml/badge.svg)](https://github.com/MoneyPack/yonderllm/actions/workflows/ci.yml)
[![CodeRabbit Pull Request Reviews](https://img.shields.io/coderabbit/prs/github/MoneyPack/yonderllm?utm_source=oss&utm_medium=github&utm_campaign=MoneyPack%2Fyonderllm&labelColor=171717&color=FF570A&link=https%3A%2F%2Fcoderabbit.ai&label=CodeRabbit+Reviews)](https://coderabbit.ai)

## Your model lives yonder.

**Bring a question. Stay in your terminal.**

A terminal workspace for remote AI: explore an idea, understand a diff, and pick
up a conversation where you left it. One executable, no model weights, no local
inference runtime. Inference runs on the provider you choose; its availability
and pricing apply.

**[Download the preview (v0.2.0-rc.2)](https://github.com/MoneyPack/yonderllm/releases/tag/v0.2.0-rc.2)**
· [Quick start](#quick-start) · [Safety and control](docs/SAFETY.md) · [Changelog](CHANGELOG.md)

- **Your terminal, your workflow.** Chat interactively or pipe a diff into a focused question.
- **Control before capability.** Chat mode has no filesystem or shell tools. Code and agent modes add them, each action approved by you.
- **A conversation you can return to.** Save, resume, and recover an interrupted answer with an explicit retry.

```powershell
# With SURPLUS_API_KEY set in your environment:
yonderllm -p surplus -m qwen3-coder-next
git diff --staged | yonderllm -p surplus -m qwen3-coder-next ask --stdin "Explain this change in two bullets."
```

---

## Install

**Prebuilt binary.** The
[v0.2.0-rc.2 preview](https://github.com/MoneyPack/yonderllm/releases/tag/v0.2.0-rc.2)
ships Windows, Linux, and macOS builds for x86-64 and ARM64 plus a
`SHA256SUMS.txt`. Download the matching file, verify it, rename it to
`yonderllm` (or `yonderllm.exe`), and put it on PATH. For Windows x86-64 start
with [yonderllm-windows-amd64.exe](https://github.com/MoneyPack/yonderllm/releases/download/v0.2.0-rc.2/yonderllm-windows-amd64.exe).
The [stable release](https://github.com/MoneyPack/yonderllm/releases/latest)
(v0.1.0) remains available.

**`go install`** (Go 1.27 or newer):

```sh
go install github.com/MoneyPack/yonderllm/cmd/yonderllm@latest
```

**From source:**

```sh
git clone https://github.com/MoneyPack/yonderllm.git
cd yonderllm
mkdir -p bin
go build -o bin/yonderllm ./cmd/yonderllm
```

Verification commands, release builds with the version stamped in, and shell
completion are in [docs/INSTALL.md](docs/INSTALL.md). A tag push builds and
publishes the six binaries via [`.github/workflows/release.yml`](.github/workflows/release.yml).

### Supported platforms

| Platform | Status |
| --- | --- |
| Linux amd64, Windows amd64, macOS amd64 | Built and tested in CI on every push |
| Linux arm64, macOS arm64 (Apple silicon), Windows arm64 | Cross-compiled for releases; not runtime-tested in CI |

Binaries are pure Go (`CGO_ENABLED=0`), unsigned and not notarized.

---

## Quick start

Set one API key and go. Groq's free tier needs no card:

```sh
export GROQ_API_KEY=gsk_...      # PowerShell: $env:GROQ_API_KEY='gsk_...'
yonderllm                         # interactive session
yonderllm ask "explain the difference between a mutex and a semaphore"
```

Check what yonderllm can see:

```
$ yonderllm providers
  PROVIDER    ROLE      MODEL  KEY                 STATUS
* groq        active    -      GROQ_API_KEY        no key
  gemini      fallback  -      GEMINI_API_KEY      no key
  openrouter  fallback  -      OPENROUTER_API_KEY  ready
  surplus     -         -      SURPLUS_API_KEY     no key

Set the listed environment variable to enable a provider.
```

The `*` marks the active provider; `ready` means a key was found. If the active
provider has no key, yonderllm says so plainly rather than failing deep inside a
request. Four providers are configured out of the box — `groq` (default),
`gemini`, `openrouter`, `surplus` — and any OpenAI-compatible endpoint can be
added in the config file. Keys are read from the environment only; the config
file names the *variable*, never the secret. Details, default models, price
tiers, and fallbacks: [docs/PROVIDERS.md](docs/PROVIDERS.md).

---

## Commands

| Command | What it does | Own flags |
| --- | --- | --- |
| *(none)* | Open the interactive TUI | |
| `ask [prompt]` | Send one prompt, stream the reply, exit; reads stdin when there is no argument | `-q, --quiet` suppress provider notices on stderr; `--stdin` append piped stdin to the prompt argument |
| `run [prompt]` | Same as `ask`, but `--json` emits newline-delimited JSON events including tool calls | `--json`; `--stdin` |
| `models [provider]` | List the free and cheap models a provider offers | `--all` include paid models; `--json` |
| `providers` | Show every provider, its role, and whether its key is set | `--json` |
| `config path` / `show` / `init` | Locate, print, or write the configuration file | `init --force` overwrite |
| `sessions` / `sessions delete <name>` | List or remove saved conversations | |
| `version` | Version, commit, build date, platform, NDJSON schema | `--json` |
| `completion <shell>` | Shell completion script for `bash`, `zsh`, `fish`, or `powershell` | |
| `help [command]` | Help for any command | |

```sh
git diff | yonderllm ask --stdin "write a commit message for this diff"
yonderllm run --json "hello" | jq -r 'select(.type=="delta").delta'
yonderllm models openrouter --all
yonderllm config show          # the effective configuration, every layer applied
yonderllm run --resume project-notes "What should I change first?"
```

`run` with no prompt, no `--json`, no `--stdin` and a terminal on stdin opens
the TUI; otherwise it is headless. `models` hides the `paid` tier by default
because paying by accident is the one failure mode a client like this must not
have. `config show` is the authority on what yonderllm actually believes.

## Flags

These persistent flags apply to every command and to the TUI.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `-p`, `--provider` | string | from config | Provider to use first |
| `-m`, `--model` | string | provider's default | Model id for the active provider |
| `--mode` | string | `chat` | Permission mode: `chat`, `code`, or `agent` |
| `--yes` | bool | off | Accept agent mode's writes and commands in advance (agent mode only; see [what it does not waive](docs/SAFETY.md#what---yes-does-and-does-not-waive)) |
| `--max-tokens` | int | from config (2048) | Cap on output tokens per request |
| `--daily-cap` | int | `-1` | Cap on requests per day; `-1` uses the config value, `0` disables the cap |
| `--config` | string | per-user config dir | Path to an alternate `config.toml` |
| `--resume` | string | | Resume a saved conversation by name |
| `--last` | bool | off | Resume the most recently saved conversation |
| `--save` | string | | Save the completed conversation under this name |
| `--no-save` | bool | off | Disable automatic conversation saving |
| `-v`, `--version` | | | Print the version and exit |
| `-h`, `--help` | | | Help for any command |

---

## Configuration

The file is optional; without it yonderllm uses its built-in defaults.

```
$YONDERLLM_CONFIG                          if set
<user config dir>/yonderllm/config.toml    otherwise
```

| OS | Path |
| --- | --- |
| Linux | `~/.config/yonderllm/config.toml` |
| macOS | `~/Library/Application Support/yonderllm/config.toml` |
| Windows | `%AppData%\yonderllm\config.toml` |

Later layers win: `built-in defaults → config file → YONDERLLM_* environment → flags`.
Only `YONDERLLM_PROVIDER`, `YONDERLLM_MODEL`, and `YONDERLLM_MODE` exist as
environment overrides; `max_tokens` and `daily_cap` are set in the file or by
flag, deliberately, because they guard your quota.

`yonderllm config init` writes a commented version of this:

```toml
provider  = "groq"
fallbacks = ["gemini", "openrouter"]
mode      = "chat"

max_tokens = 2048
daily_cap  = 200

# Optional runtime settings; see docs/PROVIDERS.md
retry_attempts = 0
retry_backoff_ms = 500
request_timeout_seconds = 0
output_format = "text"

[providers.groq]
base_url    = "https://api.groq.com/openai/v1"
api_key_env = "GROQ_API_KEY"
model       = "llama-3.3-70b-versatile"
# context_window = 131072   # tokens; lets long conversations use the model's real window

# Any OpenAI-compatible endpoint works. Omit api_key_env if it needs no key.
# [providers.local]
# base_url = "http://localhost:8080/v1"
# model    = "whatever-it-serves"
```

On Unix the file must be owner-only (`chmod 600`). `daily_cap` is a local
request counter shared across processes, not a provider quota; see
[the daily cap](docs/SAFETY.md#the-daily-cap). Runtime settings, `header_env`,
`omit_stream_options`, and `context_window` are described in
[docs/PROVIDERS.md](docs/PROVIDERS.md).

---

## The TUI

Bare `yonderllm` opens it. The transcript scrolls above; you type below. The
header shows the provider, model, and permission mode at all times.

| Key | Action |
| --- | --- |
| `enter` | Send |
| `ctrl+j` | Newline |
| `pgup` / `pgdn` | Scroll the transcript |
| `ctrl+home` / `ctrl+end` | Jump to the top or bottom of the transcript |
| `ctrl+c` | Stop a reply in flight, or quit when nothing is in flight |
| `y` | Allow the tool call that is waiting on you; any other key denies it |

```
/mode [chat|code|agent] show or change the permission mode
/retry                  retry an interrupted answer with tools disabled
/model [provider] [model]
                        show or switch provider and/or model, keeping the conversation
/clear                  forget the conversation so far
/read <file>            show a file from the working directory (code/agent only)
/search <text>          find that text in the working directory (code/agent only)
/save <name>            save this conversation so you can resume it later
/usage                  show requests used against the daily cap
/help                   show this
```

Interactive sessions autosave after each completed exchange; `--last` resumes
the newest, `--resume <name>` a named snapshot, `--no-save` turns autosave off.
`/mode` changes take effect while idle and drop `--yes`. Saved conversations,
`/retry`, and the approval prompt are described in
[docs/SAFETY.md](docs/SAFETY.md#saved-conversations) and
[docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md#recovering-an-interrupted-answer).

---

## Modes and tools

`--mode` sets how much yonderllm — and the model — may touch. Sessions start in
`chat`; nothing promotes itself out of it.

| Mode | Read / search | Write | Execute | Tools offered to the model |
| --- | --- | --- | --- | --- |
| `chat` *(default)* | ✗ | ✗ | ✗ | none |
| `code` | allow | ask | ✗ | `read_file`, `search_files`, `write_file` |
| `agent` | allow | ask | ask | `read_file`, `search_files`, `write_file`, `run_command` |

Every write and every command is shown to you — a diff, or the exact argument
vector — and runs only after you press `y`. `--yes` (agent mode only) answers
in advance, but recognised destructive or interpreter commands, writes to
hook-like files (`.githooks/`, `Makefile`, `package.json`, …), and reads of
credential-like files (`.env`, `id_rsa`, …) are still confirmed, and writes under
`.git/` are refused outright. In non-interactive runs (`run --json`, a pipe) a
tool that would need approval is withheld from the model entirely. File access
is confined to the directory yonderllm was started in via `os.Root`; commands
run as your OS user without a sandbox and do not see your configured API keys.
The full rules, limits, and what containment does *not* cover:
[docs/SAFETY.md](docs/SAFETY.md).

---

## Scripting

`run --json` emits one JSON object per line, flushed as it arrives. Every event
carries `schema_version`, `type`, and the `provider` and `model` that produced
it; a stream ends with exactly one `done` or `error`.

```sh
yonderllm run --json "count to three" | jq -rj 'select(.type=="delta").delta'
```

The event schema, tool events, exit codes, and recipes for PowerShell and
Python: [docs/SCRIPTING.md](docs/SCRIPTING.md). Versioning of the NDJSON
contract: [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md).

---

## Documentation

| | |
| --- | --- |
| [docs/INSTALL.md](docs/INSTALL.md) | Binaries, source builds, release builds, completion |
| [docs/PROVIDERS.md](docs/PROVIDERS.md) | Built-in providers, custom endpoints, runtime settings, price tiers |
| [docs/SAFETY.md](docs/SAFETY.md) | Modes, tools, approvals, `--yes`, containment, saved data, daily cap |
| [docs/SCRIPTING.md](docs/SCRIPTING.md) | `run --json` event schema and recipes |
| [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) | Errors, recovery, `/retry` |
| [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md) | Versioning, build metadata, NDJSON contract |
| [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) | Building, testing, CI, project layout, conventions |
| [docs/EVALUATION.md](docs/EVALUATION.md) | The `yonder-eval` provider evaluation runner |
| [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md) | Trust boundaries, controls, residual risks |
| [SPEC.md](SPEC.md) | Goals, non-goals, and interface contracts |
| [CHANGELOG.md](CHANGELOG.md) | What changed in each release |

Community: [contributing](CONTRIBUTING.md), [code of conduct](CODE_OF_CONDUCT.md),
[security reporting](SECURITY.md), [issues](https://github.com/MoneyPack/yonderllm/issues).

Built on [Cobra](https://github.com/spf13/cobra),
[Bubble Tea](https://github.com/charmbracelet/bubbletea), and friends — eight
direct dependencies, no cgo, one binary. [MIT licensed](LICENSE).
