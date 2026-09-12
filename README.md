# yonderllm

A terminal client for large language models that never runs one.

Inference happens *yonder* — on a provider's hardware, over the network. Your
machine draws the interface and shuttles bytes. Nothing is downloaded, nothing
is quantised, nothing warms your lap. The result is a single static binary that
starts instantly on a laptop, a Raspberry Pi, or a shell you SSH into, and that
stays inside the free and near-free tiers of the providers it speaks to.

```
$ yonderllm
```

That is the whole thing. Bare `yonderllm` opens the TUI. Everything else is a
subcommand for when you want an answer without a session.

---

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [Commands](#commands)
- [Flags](#flags)
- [Providers and API keys](#providers-and-api-keys)
- [Configuration](#configuration)
- [The TUI](#the-tui)
- [Tools](#tools)
- [Scripting with `run --json`](#scripting-with-run---json)
- [Modes](#modes)
- [Project layout](#project-layout)
- [Development](#development)

---

## Install

### From source

Requires Go 1.27 or newer.

```sh
go install yonderllm/cmd/yonderllm@latest
```

Or build from a clone:

```sh
git clone <repo> yonderllm
cd yonderllm
go build -o bin/yonderllm ./cmd/yonderllm
```

### Release build

Stripped, with the version stamped in:

```sh
go build -ldflags "-s -w -X yonderllm/internal/cli.Version=0.1.0" \
  -o bin/yonderllm ./cmd/yonderllm
```

On Windows PowerShell:

```powershell
go build -ldflags "-s -w -X yonderllm/internal/cli.Version=0.1.0" `
  -o .\bin\yonderllm.exe .\cmd\yonderllm
```

Verify:

```
$ yonderllm --version
yonderllm 0.1.0
```

### Shell completion

```sh
yonderllm completion bash   > /etc/bash_completion.d/yonderllm
yonderllm completion zsh    > "${fpath[1]}/_yonderllm"
yonderllm completion fish   > ~/.config/fish/completions/yonderllm.fish
yonderllm completion powershell | Out-String | Invoke-Expression
```

---

## Quick start

Set one API key and go. Groq's free tier needs no card:

```sh
export GROQ_API_KEY=gsk_...
yonderllm
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

The `*` marks the active provider. `ready` means a key was found in the
environment. If the active provider has no key, yonderllm says so plainly
rather than failing deep inside a request:

```
$ yonderllm models
yonderllm: groq: authentication failed: no API key: set GROQ_API_KEY
```

---

## Commands

| Command | What it does |
| --- | --- |
| *(none)* | Open the interactive TUI |
| `ask` | Send one prompt, print the reply, exit |
| `run` | Same as `ask`, but can emit machine-readable NDJSON |
| `models` | List the models the active provider offers |
| `providers` | Show every provider, its role, and whether its key is set |
| `config` | Inspect or create the configuration file |
| `completion` | Generate a shell completion script |
| `help` | Help about any command |

### `ask`

The conversational one-shot. Reads the prompt from the arguments, or from
stdin if there are none, so it composes with the rest of your shell.

```sh
yonderllm ask "explain the difference between a mutex and a semaphore"

git diff | yonderllm ask "write a commit message for this diff"

yonderllm -p gemini ask "summarise the CAP theorem"
```

| Flag | Meaning |
| --- | --- |
| `-q`, `--quiet` | Print only the reply — no provider banner, no usage footer |

### `run`

The scriptable one-shot. Identical output to `ask` by default; with `--json`
it emits one JSON object per line instead — including the model's tool calls,
which plain output leaves out.

```sh
yonderllm run "hello"
yonderllm run --json "hello" | jq -r 'select(.type=="delta").delta'
```

| Flag | Meaning |
| --- | --- |
| `--json` | Emit newline-delimited JSON events instead of prose |

### `models`

```sh
yonderllm models              # free and cheap models on the active provider
yonderllm models --all        # everything the provider advertises
yonderllm -p openrouter models
```

Every model is banded into one of four tiers by the price the provider
advertises, measured in US dollars per million tokens:

| Tier | Meaning |
| --- | --- |
| `free` | Both input and output are priced at zero |
| `cheap` | Both input and output are at or under $1.00 per million tokens |
| `paid` | Either side costs more than that |
| `unknown` | The provider advertised no usable price |

yonderllm hides the `paid` tier by default, because paying by accident is the
one failure mode a client like this must not have. `free`, `cheap`, and
`unknown` all pass the filter — an unpriced model is shown, and labelled
honestly, rather than silently dropped. When the filter leaves nothing, it tells
you rather than printing an empty table:

```
$ yonderllm -p openrouter models
openrouter reports no free or cheap models. Try --all.
```

The table prints both halves of the price, input first:

```
$ yonderllm -p surplus models
  MODEL                NAME          CONTEXT  PRICE/1M     TIER
* openai-gpt-oss-120b  GPT OSS 120B  125K     0.07 / 0.30  cheap
  gratis-8b            Gratis 8B     32K      0 / 0        free
  silent-rates         Silent Rates  8K       unknown      unknown

* active model for surplus
```

### `config`

```sh
yonderllm config path    # where the config file would be read from
yonderllm config show    # the effective configuration, after all overrides
yonderllm config init    # write a commented starter file
```

`config show` is the authority on what yonderllm actually believes, with every
layer of precedence already applied. Reach for it before assuming a flag or an
environment variable did what you meant.

---

## Flags

These apply to every command and to the TUI.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `-p`, `--provider` | string | from config | Provider to use |
| `-m`, `--model` | string | provider's default | Model to use |
| `--mode` | string | `chat` | Permission mode: `chat`, `code`, or `agent` |
| `--max-tokens` | int | from config | Cap the reply length |
| `--daily-cap` | int | `-1` | Max requests per day; `-1` means use the config value |
| `--config` | string | see below | Path to an alternate config file |
| `-v`, `--version` | | | Print the version and exit |
| `-h`, `--help` | | | Help for any command |

---

## Providers and API keys

Four providers are configured out of the box. Each is reached over HTTP, and
none of them is asked for a card by yonderllm.

| Provider | Environment variable | Base URL | Suggested model |
| --- | --- | --- | --- |
| `groq` *(default)* | `GROQ_API_KEY` | `https://api.groq.com/openai/v1` | `llama-3.3-70b-versatile` |
| `gemini` | `GEMINI_API_KEY` | `https://generativelanguage.googleapis.com/v1beta` | `gemini-2.0-flash` |
| `openrouter` | `OPENROUTER_API_KEY` | `https://openrouter.ai/api/v1` | `meta-llama/llama-3.3-70b-instruct:free` |
| `surplus` | `SURPLUS_API_KEY` | `https://api.surplusintelligence.ai/v1` | `gpt-5.6-sol` |

Keys are read from the environment only. yonderllm never writes a key to its
config file, and the config file names the *variable*, not the secret — so it
is safe to commit or sync.

### Fallbacks

Providers form a chain. The first one with a key becomes active; the others
stand by. If the active provider fails a request, yonderllm moves down the
chain rather than surfacing the error, and reports the substitution as a
notice. Reorder the chain with `fallbacks` in the config file.

### Any OpenAI-compatible endpoint

The provider list is not closed. Anything that speaks the OpenAI chat
completions API — another hosted service, or a local server if you decide you
want one after all — can be added as a `[providers.<name>]` block. Omit
`api_key_env` for a server that needs no key.

---

## Configuration

### Location

```
$YONDERLLM_CONFIG            if set
<user config dir>/yonderllm/config.toml   otherwise
```

Which resolves to:

| OS | Path |
| --- | --- |
| Linux | `~/.config/yonderllm/config.toml` |
| macOS | `~/Library/Application Support/yonderllm/config.toml` |
| Windows | `%AppData%\yonderllm\config.toml` |

Ask rather than guess:

```sh
yonderllm config path
```

The file is optional. Without it, yonderllm uses its built-in defaults.

### Precedence

Later layers win:

```
built-in defaults  →  config file  →  YONDERLLM_* environment  →  command-line flags
```

Only three settings can be overridden by environment variable:

| Variable | Overrides |
| --- | --- |
| `YONDERLLM_PROVIDER` | active provider |
| `YONDERLLM_MODEL` | model |
| `YONDERLLM_MODE` | permission mode |

(`YONDERLLM_CONFIG` selects the file itself, and so sits outside the chain.)
`max_tokens` and `daily_cap` are set in the config file or by flag — there is
deliberately no environment override for the two settings that guard your
quota.

### Example

`yonderllm config init` writes a commented version of this:

```toml
provider  = "groq"
fallbacks = ["gemini", "openrouter"]
mode      = "chat"

max_tokens = 2048
daily_cap  = 200

[providers.groq]
base_url    = "https://api.groq.com/openai/v1"
api_key_env = "GROQ_API_KEY"
model       = "llama-3.3-70b-versatile"

[providers.gemini]
base_url    = "https://generativelanguage.googleapis.com/v1beta"
api_key_env = "GEMINI_API_KEY"
model       = "gemini-2.0-flash"

[providers.openrouter]
base_url    = "https://openrouter.ai/api/v1"
api_key_env = "OPENROUTER_API_KEY"
model       = "meta-llama/llama-3.3-70b-instruct:free"

[providers.surplus]
base_url    = "https://api.surplusintelligence.ai/v1"
api_key_env = "SURPLUS_API_KEY"
model       = "gpt-5.6-sol"

# Any OpenAI-compatible endpoint works. Omit api_key_env if it needs no key.
# [providers.local]
# base_url = "http://localhost:8080/v1"
```

### The daily cap

`daily_cap` is a local counter, not a provider feature. yonderllm tracks how
many requests it has made today and refuses the one that would exceed the cap:

```
daily request cap reached (200/200), resets at 00:00
```

It exists because free tiers punish enthusiasm quietly and cheap tiers bill it.
`/usage` in the TUI shows where you stand.

---

## The TUI

Bare `yonderllm` opens it. The transcript scrolls above; you type below.

### Slash commands

```
/model                 show the provider and model in use
/model <model>         switch model on the current provider
/model <provider>      switch provider, keeping its configured model
/model <provider> <model>
                       switch both at once
/clear                 forget the conversation so far
/read <file>           show a file from the workspace
/search <text>         find a literal string across the workspace
/usage                 show requests used against the daily cap
/help                  show this
```

`/read` and `/search` are governed by `--mode`: both are refused in `chat` and
allowed in `code` and `agent`. Neither ever prompts, so the permission model is
enforced end to end without an approval dialog. Paths are relative to the
directory yonderllm was started in and cannot escape it.

### Keys

| Key | Action |
| --- | --- |
| `enter` | Send |
| `ctrl+j` | Newline |
| `pgup` / `pgdn` | Scroll the transcript |
| `ctrl+c` | Stop a reply in flight, or quit |

`ctrl+c` is deliberately overloaded: the first press interrupts a stream, and a
press with nothing in flight exits. You never need a second key to escape a
runaway answer.

Switching model with `/model` keeps the conversation. Only `/clear` discards it.

---

## Tools

`/read` and `/search` are you looking at the project. Tools are the *model*
looking at it. In `code` and `agent` mode yonderllm tells the model which
capabilities it has, and when the model asks for one, yonderllm runs it and
feeds the result back — without leaving the turn you are already in.

| Tool | What the model gets |
| --- | --- |
| `read_file` | One UTF-8 text file, addressed relative to the project root |
| `search_files` | Every line in the project matching a literal string, with its path |

Both take a single argument and are described to the model by JSON Schema, so a
malformed call is refused with a sentence the model can act on rather than
silently mishandled:

```json
{"name": "read_file",    "arguments": {"path":  "internal/cli/run.go"}}
{"name": "search_files", "arguments": {"query": "func newSessionFor"}}
```

Search is literal and case-insensitive — no regular expressions, because a
model that guesses at a regex dialect wastes a round trip finding out which one
it got.

### What the model is told

The tool list is built from the permission policy, not filtered after the fact.
A capability the current mode does not allow is **never described to the
model** — it cannot ask for what it has not been offered, so a refusal is not
something the model has to be talked out of.

| Mode | Tools offered |
| --- | --- |
| `chat` | none |
| `code` | `read_file`, `search_files` |
| `agent` | `read_file`, `search_files` |

Only capabilities the mode marks *allow* are offered. Anything the mode would
merely *ask* about is withheld, because nothing in this loop can raise a prompt
yet. That is why reading and searching came first: they are `allow` in both
`code` and `agent`, so the tool loop needed no approval dialog to be correct.

### The loop

A turn is a conversation, not a single request. The model may answer, or it may
call tools; if it calls, yonderllm runs them in order, appends each result, and
asks again. The loop is bounded at **six rounds**. On the last round the tools
are withheld, which turns the ceiling into a prose answer instead of a
truncation — the model is asked to conclude with what it has rather than cut
off mid-investigation.

Each round is one request against the daily cap, and token usage accumulates
across the whole turn so the footer reports the true cost of the answer, not
just its final leg.

Results are capped at 8 KiB and clipped on a line boundary, with a note saying
how many lines were dropped. A tool result goes straight into the next prompt,
so an unbounded one would spend your context window on a file the model only
needed to glance at.

### What you see

Tool calls are not hidden. Each one lands in the transcript as its own block —
the tool's name, the arguments it was called with, and the result once it
returns, clipped for display:

```
tool search_files
{"query": "parseFlags"}
  3 matches for "parseFlags"
  cmd/app/main.go:24: flags, err := parseFlags(os.Args[1:])
  internal/app/flags.go:31: // parseFlags reads an argument list into a Flags.
  internal/app/flags.go:36: func parseFlags(args []string) (Flags, error) {

tool read_file
{"path": "internal/app/flags.go"}
  internal/app/flags.go
  package app

  import "flag"

  ... 26 more lines
```

An answer that leans on a file you did not expect is visible as it happens,
which is the difference between a tool loop you can trust and one you have to
audit afterwards.

Scripts see the same thing. `run --json` reports each call as a `tool` event
and each outcome as a `tool_result`, described in the [event
schema](#event-schema) below. Plain `run` prints only the answer, so a tool
loop never disturbs output something else is already parsing.

### Containment

Every read and every search goes through `internal/workspace`, which holds an
`os.Root` on the directory yonderllm was started in. Containment is a property
of the handle, not a string check performed hopefully at the top of a function.

| Refused | Because |
| --- | --- |
| Absolute paths | Addressable only inside the project |
| Anything reaching `..` past the root | Escape, rejected before any syscall |
| Windows device names such as `NUL`, `COM1` | Not files, however they resolve |
| Binary files | Sniffed, not trusted by extension |
| Files over 1 MiB | A prompt is not a place to put a megabyte |

Searches stop at 200 matches and skip the directories nobody means to search —
`.git`, `node_modules`, `vendor`, build output. Unreadable files are skipped
rather than aborting the walk: one permission error in a tree should not cost
you the other 900 results.

---

## Scripting with `run --json`

`run --json` emits newline-delimited JSON — one object per line, flushed as it
arrives, so a pipe stays live for the whole stream.

```sh
yonderllm run --json "count to three"
```

```json
{"type":"notice","notice":"groq unavailable, using gemini","provider":"gemini","model":"gemini-2.0-flash"}
{"type":"delta","delta":"one"}
{"type":"delta","delta":", two"}
{"type":"delta","delta":", three"}
{"type":"done","provider":"gemini","model":"gemini-2.0-flash","usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}
```

### Event schema

| Field | Type | Present on | Meaning |
| --- | --- | --- | --- |
| `type` | string | all | `delta`, `notice`, `tool`, `tool_result`, `done`, or `error` |
| `delta` | string | `delta` | The next fragment of the reply |
| `notice` | string | `notice` | Something worth knowing, such as a fallback |
| `provider` | string | all | Provider that served the request |
| `model` | string | all | Model that served the request |
| `tool` | object | `tool`, `tool_result` | The call, and then how it ended |
| `error` | string | `error` | What went wrong |
| `usage` | object | `done` | `prompt_tokens`, `completion_tokens`, `total_tokens` |

A stream ends with exactly one `done` or one `error`. Notices are informational
and never terminal, which means a consumer can ignore every type it does not
recognise and still be correct.

Every event carries `provider` and `model`, so a line is meaningful on its own
even when a fallback changed which provider was answering partway through. The
examples here elide both for readability.

### Tool events

A call and its outcome are two events, not one, because the call is worth
showing before the work has finished. They share an `id`, so a consumer can pair
them without depending on adjacency:

```json
{"type":"tool","tool":{"id":"call_1","name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}
{"type":"tool_result","tool":{"id":"call_1","name":"read_file","arguments":"{\"path\":\"go.mod\"}","result":"go.mod\nmodule yonderllm\n"}}
```

| Field | Type | Meaning |
| --- | --- | --- |
| `id` | string | Ties a `tool_result` to its `tool` |
| `name` | string | `read_file` or `search_files` |
| `arguments` | string | The JSON the model sent, verbatim |
| `result` | string | What the tool returned, on success |
| `error` | string | Why the tool refused, instead of `result` |

`arguments` is a string and not an object on purpose: it is the model's own
JSON, passed through unaltered. yonderllm will not reformat a malformed call
into something that looks valid, so what you see is what the model actually
asked for.

A `tool_result` carries exactly one of `result` or `error`. A refused call is
not a failed turn — the message goes back to the model, which usually corrects
itself and answers, and the stream still ends in `done`.

Watch what the model reads:

```sh
yonderllm run --mode code --json "$PROMPT" \
  | jq -r 'select(.type=="tool") | "\(.tool.name) \(.tool.arguments)"'
```

Reassemble a reply:

```sh
yonderllm run --json "$PROMPT" | jq -rj 'select(.type=="delta").delta'
```

Fail a script on a provider error:

```sh
yonderllm run --json "$PROMPT" \
  | jq -e 'select(.type=="error") | halt_error(1)' >/dev/null
```

---

## Modes

`--mode` sets how much yonderllm is permitted to touch.

| Mode | Read / search | Write | Execute |
| --- | --- | --- | --- |
| `chat` *(default)* | ✗ | ✗ | ✗ |
| `code` | allow | ask | ✗ |
| `agent` | allow | ask | ask |

The policy is enforced in one place, `internal/perm`, so the rules can be read
and tested as a unit rather than inferred from call sites. Escalation is always
an explicit act: nothing promotes itself out of `chat`.

Reading and searching are live in both directions. You reach them with `/read`
and `/search`; the model reaches them as [tools](#tools). Both directions go
through the same `perm.Policy`, so a mode that refuses you refuses the model
too — and a capability the mode does not allow is never even described to the
model.

> **Status.** Write and execute are not built yet, so the `ask` cells in the
> table above describe the policy the remaining tools will be wired into, not
> capabilities that exist today. No mode writes a file or runs a command. That
> also means `code` and `agent` currently offer the model the same two tools;
> they diverge once write and execute arrive with the approval prompt they
> require.

---

## Project layout

```
cmd/yonderllm/      entry point; nothing but wiring
internal/cli/       commands, flags, and the defaults → config → env → flags resolver
internal/config/    the config file, its paths, and its precedence rules
internal/provider/  HTTP clients for OpenAI-compatible endpoints
internal/session/   conversation state, streaming, fallback chain, usage cap
internal/perm/      the permission policy
internal/tools/     the capabilities the model is offered, gated by that policy
internal/tui/       the Bubble Tea interface
internal/workspace/ containment-checked file reads and searches
```

Built on [Cobra](https://github.com/spf13/cobra),
[Bubble Tea](https://github.com/charmbracelet/bubbletea),
[Bubbles](https://github.com/charmbracelet/bubbles),
[Lip Gloss](https://github.com/charmbracelet/lipgloss), and
[BurntSushi/toml](https://github.com/BurntSushi/toml). Five direct
dependencies, no C, one binary.

---

## Development

```sh
go vet ./...
go test ./...
```

On Windows, if Go is installed but not on `PATH`:

```powershell
& "C:\Program Files\Go\bin\go.exe" vet ./...
& "C:\Program Files\Go\bin\go.exe" test ./...
```

### Cross-compiling

```sh
GOOS=linux  GOARCH=amd64 go build -o dist/yonderllm-linux-amd64  ./cmd/yonderllm
GOOS=linux  GOARCH=arm64 go build -o dist/yonderllm-linux-arm64  ./cmd/yonderllm
GOOS=darwin GOARCH=arm64 go build -o dist/yonderllm-darwin-arm64 ./cmd/yonderllm
```

No cgo, so every target cross-compiles from any host.

### Conventions

- Every file opens with a prose doc comment explaining what it is for and why
  it is separate from its neighbours.
- Tests assert on substrings, not exact output, so that wording can improve
  without a test rewrite.
- `SPEC.md` holds the goals, non-goals, and interface contracts. It is the
  document to change first when the design changes.
