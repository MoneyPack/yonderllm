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
| Headless | `yonderllm ask --json "..."` | scripting, piping into other tools |

### CLI subcommands

- `ask <prompt>` — one-shot completion. `--json` for machine-readable output.
- `models` — list free and cheap models on the active provider. `--all` includes
  models priced above the cheap tier; `--json` for machine-readable output.
- `providers` — list configured providers and their credential status.
- `config` — show resolved config and its source (file vs env vs default).
- `run` — start the TUI explicitly (same as bare invocation).

### Slash commands (TUI)

`/model`, `/clear`, `/read`, `/search`, `/usage`, `/help`.

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
- On a quota or rate-limit error, fall back to the next configured provider
  automatically and note the switch in the transcript.

## 8. Configuration

Resolution order — later wins:

1. Built-in defaults
2. Config file (TOML)
3. Environment variables
4. Command-line flags

API keys come **only** from environment variables or a config file whose
permissions restrict it to the owning user. Keys are never printed, never
logged, and never included in error messages or crash output.

## 9. Testing

- **Adapters** — unit tests against recorded provider responses; no live calls
  in CI.
- **Permissions** — policy tests asserting each mode denies what it must deny.
- **Config** — golden tests for parsing, precedence, and provider fallback
  ordering.
