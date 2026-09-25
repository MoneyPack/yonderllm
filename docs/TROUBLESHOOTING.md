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

- **Missing model:** the compiled-in default for a provider can be retired
  between releases. `yonderllm -p <provider> models --all` lists what it
  serves now; override with `--model`, `YONDERLLM_MODEL`, or `model` under
  `[providers.<name>]`. See [providers](PROVIDERS.md#built-in-providers).
- **`--yes` refused:** it is accepted only with `--mode agent` (or `mode =
  "agent"` in config). In the TUI, `/mode agent` does not restore `--yes`;
  restart with the flag if you want auto-approval.
- **Tool missing from the model's list:** in a non-interactive run (`run
  --json`, a pipe) tools that need approval are withheld because nobody can
  answer. Use the TUI, or `--mode agent --yes` for scripts. Recognised
  destructive commands, hook-like writes and credential-like reads are still
  refused without an interactive confirmation.

## Recovering an interrupted answer

Use `/retry` after a failed or cancelled exchange has stopped. The retry sends
the existing conversation again without duplicating your question. Completed
tool results remain in context, but **all tools are disabled for the retry**:
it requests an answer only and cannot repeat file writes or commands. To request
new actions, send a new prompt after reviewing the previous results.

Partial output stays visible in the transcript; the retried answer starts again
from the saved conversation context rather than continuing those partial words.
If cancellation left tool calls without recorded results, retry is refused:
review the workspace and use `/clear` to start a new conversation. Successful
answers, including those whose autosave failed, cannot be repeated with `/retry`.
Retry state is in-memory and clears on `/clear` or resume.

Each retry reserves a request against the daily cap. Partially answered attempts
and exchanges that reached tool execution keep their original reservation.
Provider error frames inside HTTP 200 streams are reported as errors, and EOF
without a finish marker or `[DONE]` is treated as an interruption. Automatic
provider fallback stops once partial text has arrived, avoiding mixed answers.

When reporting a problem, include version, OS, command/flags and sanitized error
text. Do not attach keys, full private conversation files, or `.env` files.
