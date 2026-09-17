# yonderllm

> your model lives yonder

A terminal LLM client written in Go. Inference always runs remotely — the local
machine only draws a terminal UI and shuttles bytes. Single static binary, no
runtime dependencies, no VPS, friendly to free and cheap tiers.

## 1. Goals

1. **Zero local inference.** No model weights, no GPU requirement. The client is
   a thin transport + UI layer.
2. **Near-zero running cost.** Default to free and cheap models — anything at or
   under $1 per million tokens on both input and output; fall back
   automatically when a quota is exhausted.
3. **Provider-agnostic.** One adapter interface; many backends.
4. **Safe by default.** Chat mode has no filesystem or shell access. Escalation
   is explicit.
5. **Single binary.** `go build` produces one executable; config is optional.

## 2. Non-goals

- Running models locally (talking to a *local server* like Ollama is supported;
  bundling inference is not).
- Hosting a service. There is no server component.
- Being an IDE. Workspace access is scoped and patch-oriented.

## 3. Repository layout

```
cmd/yonderllm/        entry point, flag parsing, mode selection
internal/tui/         full-screen terminal UI
internal/cli/         subcommands: ask, models, providers, config, run
internal/provider/    adapter interface + per-provider implementations
internal/session/     conversation history, context trimming, token accounting
internal/perm/        permission modes and policy enforcement
internal/workspace/   scoped read / search / patch operations
internal/config/      TOML parsing, env key resolution, provider registry
```

## 4. Interfaces

Three ways to drive the same core:

| Interface | Invocation | Use case |
|---|---|---|
| TUI | `yonderllm` | interactive full-screen session |
| CLI | `yonderllm ask "..."` | one-shot from a shell |
| Headless | `yonderllm run --json "..."` | scripting, piping into other tools |

### CLI subcommands

- `ask <prompt>` — one-shot text completion. `run --json` for machine-readable output.
- `models` — list free and cheap models on the active provider. `--all` includes
  models priced above the cheap tier; `--json` for machine-readable output.
- `providers` — list configured providers and their credential status.
- `config` — show resolved config and its source (file vs env vs default).
- `run` — start the TUI explicitly (same as bare invocation).

### Slash commands (TUI)

`/model`, `/clear`, `/read`, `/search`, `/usage`, `/help`.

`/mode [chat|code|agent]` reports or changes permissions while idle, rebuilding
the tool set and header together without clearing history. A change uses
per-call approval rather than carrying `--yes` into the new mode. Selecting the
current mode is a no-op. Changes wait for cancelled workers to finish and never
interrupt an approval question. Previously read context remains in history.

`/retry` retries a failed/interrupted exchange after the worker stops, reusing
existing messages without duplicating the user turn. Tools are withheld and
unsolicited calls are not executed. Completed tool results remain context;
unmatched tool calls prevent retry. A new daily-cap reservation is required.
Completed answers with save failures are not retryable. Retry state is not
persisted and is cleared by clear/load. Stream error envelopes are surfaced;
unmarked EOF is a protocol interruption, and partial text prevents fallback.

`/save <name>` writes a named conversation snapshot. Interactive sessions
autosave completed exchanges; `--no-save` disables autosave. `--resume <name>`
and `--last` restore saved context in the TUI or before a headless prompt.
`--save <name>` opts a one-shot exchange into persistence. `sessions` lists
valid snapshots by save time; `sessions delete <name>` removes one.

Snapshots are versioned JSON beside configuration, outside the cache. Writes
are locked and installed through temporary-file replacement. Recognized secrets
are redacted at rest, including the system prompt and structured tool arguments;
this is best-effort redaction, not encryption. Resume restores provider/model
and conversation history, never permissions, approvals, credentials, or usage.
Incomplete tool exchanges are rejected rather than replayed. Each resumed run
uses a new autosave unless an explicit save name is supplied.

`/read` and `/search` are the first commands gated by the permission policy:
both call `perm.Policy.Check` through `internal/workspace` and are refused in
`chat` mode.

## 5. Providers

Native adapters:

- **Groq** — primary default, free tier, fast.
- **Gemini** — Google AI Studio free tier.
- **OpenRouter** — aggregates many free and cheap models.
- **Surplus** — [OI]-compatible endpoint at `api.surplusintelligence.ai`.

Generic adapters:

- **OpenAI-compatible** — any base URL exposing `/v1/chat/completions`.
- **Local/remote servers** — Ollama, LM Studio, llama.cpp server, vLLM. These
  reach a server over HTTP; the client itself still performs no inference.

### Adapter interface

Every provider implements the same contract:

- `Name() string`
- `Models(ctx) ([]Model, error)`
- `Stream(ctx, Request) (iter of Chunk, error)`

`Request` carries messages, model id, max output tokens, and temperature.
`Chunk` carries a delta, a finish reason, and optional usage counters.
Provider-specific quirks stay inside the adapter; the core sees one shape.

## 6. Permission modes

Mode is displayed prominently at all times. Sessions start in **Chat**.

| Mode | Filesystem | Shell | Approval |
|---|---|---|---|
| **Chat** | none | none | n/a |
| **Code** | read + search; proposes patches | none | required before any write |
| **Agent** | read, search, write | yes | configurable; destructive commands always confirmed |

Rules that hold in every mode:

- All paths are resolved and confined to the workspace root. Escapes are denied.
- Obvious secrets (API keys, tokens, `.env` values) are redacted before any
  content reaches a provider.
- Destructive shell commands require an explicit confirmation regardless of the
  configured approval level.

### Approval

A mode that answers **Ask** does not decide anything by itself; it defers to the
person at the terminal. Asking needs an interface, so the policy does not carry
the question — the caller that owns an interface supplies an *approver*, and the
tool layer calls it the moment a model requests a gated action.

The prompt states the action, its target, and enough of the change to judge it:
a diff for a write, the exact argument vector for an exec. Rules:

- **Deny is the default.** An empty answer, an interrupt, a closed input, or a
  cancelled context all mean no.
- **A denial is not an error.** It goes back to the model as a tool result
  saying the user refused, so it can propose something else instead of retrying
  blindly.
- **Approval is per call, never remembered.** Approving one write does not
  approve the next. Only `agent` mode with auto-approval configured skips the
  prompt, and destructive commands are confirmed even then.

Where no approver exists — `--json` output, a pipe, any non-interactive run —
a capability the mode would ask about is withheld from the model entirely
rather than silently allowed or offered and then refused. Scripts that want
writes must ask for them explicitly with `--yes`, which is accepted only in
`agent` mode.

## 7. Cost control

- Models are banded by price: **free** (both rates zero), **cheap** (both rates
  at or under $1 per million tokens), **paid** (anything dearer), and
  **unknown** (the provider published no rates). `models` lists free and cheap
  only; `--all` lifts the filter.
- Free and cheap providers are the defaults; small models are tried first.
- Context is trimmed to fit the model window, oldest turns first.
- Output tokens are capped per request.
- A daily request cap plus a running usage counter, surfaced by `/usage`.
  The daily request count is persisted in the user cache and shared across
  processes using a locked read/modify/write transaction. Reservations are
  saved before dispatch; storage errors refuse the request. The count rolls
  over at local midnight; late refunds never reduce the new day's count.
  Token totals remain session-local. Clearing the cache resets the local count.
- On a quota or rate-limit error, fall back to the next configured provider
  automatically and note the switch in the transcript.

## 8. Configuration

Resolution order — later wins:

1. Built-in defaults
2. Config file (TOML)
3. Environment variables
4. Command-line flags

API keys come **only** from environment variables; config holds their variable
names. Unix config files must be owner-only (0600); Windows uses ACLs. Provider
diagnostics redact configured key/header values and recognized key-shaped text.
Redaction is best effort, not an assurance that arbitrary private text is safe.

Runtime options: `retry_attempts` (0–3, default 0), `retry_backoff_ms` (0–30000,
default 500), `request_timeout_seconds` (0–3600, default 0/disabled), and
`output_format` (`text` or `json`, default text, applies to `run`). Retries only
repeat a quota/5xx-failed model round before text or calls arrive; completed tool
results are reused without executing them again. Long Retry-After hints skip
same-provider retry. Explicit `daily_cap = 0` disables the local cap.

Providers accept `omit_stream_options` and environment-backed X-prefixed metadata
headers via `header_env`. Redirects and URLs with credentials/query/fragment are
refused. Custom compatible providers require no new compiled adapter.

## Compatibility

`run --json` emits schema 1 events with `schema_version: 1`. Failed output writes
stop the exchange with a nonzero exit. `version --json` exposes version, build,
platform, Go and protocol metadata. See [compatibility](docs/COMPATIBILITY.md).

## 9. Testing

- **Adapters** — unit tests against recorded provider responses; no live calls
  in CI.
- **Permissions** — policy tests asserting each mode denies what it must deny.
- **Config** — golden tests for parsing, precedence, and provider fallback
  ordering.
