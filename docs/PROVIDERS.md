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
