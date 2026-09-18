# Troubleshooting

Start with `yonderllm config show` and `yonderllm providers`. These identify the
effective configuration and key variable names, not key values.

- **Authentication:** Set the listed environment variable in this terminal.
  Check that the provider still accepts the key; rotating it is provider-managed.
- **Quota:** Wait for the provider's Retry-After hint, select another provider,
  or review its allowance. The local daily cap is separate from provider billing.
- **No model:** Set `--model` or `model` in that provider's configuration.
- **Unavailable:** The provider returned a server-side failure. A configured
  fallback can answer if no text has arrived yet.
- **Stream interrupted:** Heartbeats are not completion. Error envelopes and EOF
  without a completion marker are errors. Review the partial answer, then `/retry`.
- **Config permissions:** On Unix use `chmod 600` on the path printed by
  `config path`. Windows access is controlled by ACLs.
- **Redirect:** Configure the final API endpoint, not a redirecting gateway URL.
- **Saving failed:** The answer may already be complete. Use `/save name` after
  correcting storage permissions; `/retry` will not repeat a completed answer.
- **Output limit:** A notice explains when the provider reports hitting its
  output-token cap. The partial answer stays in history. Ask to continue, or
  increase `--max-tokens` on the next invocation. This is a completed, capped
  response rather than a failed exchange, so `/retry` is not enabled.

`/retry` waits for the worker to stop, retains the question and recorded tool
results, and disables tools. It does not rerun writes or commands. An unmatched
tool call prevents retry: inspect the workspace and start a fresh `/clear`
conversation. Retry is explicit and uses the daily request budget.

When reporting a problem, include version, OS, command/flags and sanitized error
text. Do not attach keys, full private conversation files, or `.env` files.
