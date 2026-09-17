# Providers and compatibility

Groq, Gemini, OpenRouter and Surplus use the common chat-completions adapter.
Use `yonderllm providers` to inspect credential readiness, then
`yonderllm -p groq models --all` to inspect the current remote catalog.
Model availability and pricing can change; check the provider before use.

Add another compatible service without recompiling:

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
reject the optional usage request. Provider metadata headers use environment
variable names, not literal values:

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
