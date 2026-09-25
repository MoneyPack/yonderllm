# Providers and compatibility

Groq, Gemini, OpenRouter and Surplus use the common chat-completions adapter.
Use `yonderllm providers` to inspect credential readiness, then
`yonderllm -p groq models --all` to inspect the current remote catalog.
Model availability and pricing can change; check the provider before use.

## Built-in providers

Four providers are configured out of the box. Each is reached over HTTP, and
none of them is asked for a card by yonderllm.

| Provider | Environment variable | Base URL | Default model |
| --- | --- | --- | --- |
| `groq` *(default)* | `GROQ_API_KEY` | `https://api.groq.com/openai/v1` | `llama-3.3-70b-versatile` |
| `gemini` | `GEMINI_API_KEY` | `https://generativelanguage.googleapis.com/v1beta/openai` | `gemini-2.0-flash` |
| `openrouter` | `OPENROUTER_API_KEY` | `https://openrouter.ai/api/v1` | `meta-llama/llama-3.3-70b-instruct:free` |
| `surplus` | `SURPLUS_API_KEY` | `https://api.surplusintelligence.ai/v1` | `gpt-5.6-sol` |

Keys are read from the environment only. yonderllm never writes a key to its
config file, and the config file names the *variable*, not the secret, so it
is safe to commit or sync.

**Default models may lag the provider's catalogue.** The defaults are compiled
in and change only with a release; providers retire and rename models on their
own schedule, so a default can stop existing between releases. If a request
fails with a missing-model error, list what the provider currently offers and
override the default:

```sh
yonderllm -p groq models --all            # what the provider serves right now
yonderllm -p groq -m <model-id> ask "…"   # one run
export YONDERLLM_MODEL=<model-id>         # this shell
```

or set `model = "<model-id>"` under `[providers.groq]` in the config file
(`yonderllm config init` writes the tables ready to edit). `/model <model>` in
the TUI switches for the current session. `--model` applies to the active
provider only; each fallback provider uses its own configured model.

### Fallbacks

Providers form a chain. The first one with a key becomes active; the others
stand by. If the active provider fails a request before any text has arrived,
yonderllm moves down the chain rather than surfacing the error, and reports the
substitution as a notice. Reorder the chain with `fallbacks` in the config file.

### Model price tiers

Every model `models` lists is banded into one of four tiers by the price the
provider advertises, measured in US dollars per million tokens:

| Tier | Meaning |
| --- | --- |
| `free` | Both input and output are priced at zero |
| `cheap` | Both input and output are at or under $1.00 per million tokens |
| `paid` | Either side costs more than that |
| `unknown` | The provider advertised no usable price |

yonderllm hides the `paid` tier by default, because paying by accident is the
one failure mode a client like this must not have. `free`, `cheap`, and
`unknown` all pass the filter: an unpriced model is shown and labelled
honestly rather than silently dropped. When the filter leaves nothing, it says
so rather than printing an empty table:

```
$ yonderllm -p openrouter models
openrouter reports no free or cheap models. Try --all.
```

The table prints both halves of the price, input first, and the context window
the provider reports:

```
$ yonderllm -p surplus models
  MODEL                NAME          CONTEXT  PRICE/1M     TIER
* openai-gpt-oss-120b  GPT OSS 120B  125K     0.07 / 0.30  cheap
  gratis-8b            Gratis 8B     32K      0 / 0        free
  silent-rates         Silent Rates  8K       unknown      unknown

* active model for surplus
```

`models --json` emits the same catalogue as a JSON object
(`.models[].id`, `.models[].context_window`, prices, tier) for scripts.

## Custom OpenAI-compatible endpoints

The provider list is not closed. Anything that speaks the OpenAI chat
completions API — another hosted service, or a local server — can be added as a
`[providers.<name>]` block without recompiling:

```toml
provider = "custom"
fallbacks = []

[providers.custom]
base_url = "https://your-provider.example/v1"
api_key_env = "CUSTOM_API_KEY"
model = "your-model-id"
```

Optional runtime settings, at the top of the config before provider tables:

```toml
retry_attempts = 0
retry_backoff_ms = 500
request_timeout_seconds = 0
output_format = "text"
```

Retries are opt-in, capped at three per provider round, and only apply to quota
or server-unavailable errors before text or tool calls arrive. They never rerun
completed tools. Backoff is 0–30,000 ms; larger Retry-After hints skip local retry
and allow fallback. Remote attempts may each be billed even though the local
daily counter measures logical exchanges. The whole-exchange timeout is 0–3,600
seconds; zero disables it. `output_format` controls `run`; `--json=false` overrides
it, and `ask` remains plain text.

Under a provider table, `omit_stream_options = true` supports strict servers that
reject the optional usage request. `context_window = 131072` (tokens) tells the
client the model's real window so long conversations are trimmed to it rather
than to a conservative 8192-token default; leave it out when unsure. Provider
metadata headers use environment variable names, not literal values:

```toml
[providers.custom.header_env]
X-Project = "CUSTOM_PROJECT"
```

Only X-prefixed headers are accepted, so credentials and protocol headers cannot
be overridden. Header values are environment-resolved and redacted from provider
errors. The stable extension points are provider configuration and subprocess
NDJSON; no plugin code is loaded into the client.

Replace the endpoint/model with values from your service. `base_url` must be
HTTP(S) without userinfo, query parameters or fragments. It must already include
the API version prefix; the adapter appends `/models` and `/chat/completions`.
Redirects are rejected. Use the final endpoint directly. For an unauthenticated
local server omit `api_key_env`; inference still runs in the separate server.

Fallback order follows `fallbacks`. Quota, authentication, missing-model and
provider-5xx failures can move to the next backend before any text arrives.
Transport/protocol failures stop the exchange. Partial text always stops fallback
to avoid combining answers. Notices explain the category without echoing remote
error text. Each provider uses its own configured model.

Compatibility requires SSE `data:` frames and an explicit finish marker or
`[DONE]`. Tool fragments are assembled before execution; unfinished streams do
not release pending calls. See [recovery](TROUBLESHOOTING.md).
