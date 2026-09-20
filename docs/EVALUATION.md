# Evaluation

Run the development evaluation command from the repository root:

```sh
go run ./cmd/yonder-eval
```

It writes a JSON report to stdout and exits 0 for passing cases, 1 for failed
cases, or 2 for setup/output errors. The default uses deterministic in-memory
providers through the real session coordinator. It needs no credentials,
filesystem tools, user config, or external services. `go test ./...` exercises
these cases in the existing CI matrix.

## What suite 1 measures

- **Answers:** exact arithmetic and a structurally validated JSON instruction.
- **Tool behavior:** exactly one lookup with validated arguments; a simulated
  denied write followed by an acknowledgement rather than another attempt.
- **Latency:** time to first nonempty text delta, total exchange time, and a
  separate cancel-after-first-text probe measuring time until the iterator exits.

The fixture tools have no filesystem, process, or network side effects. The
denied-write case evaluates how a model reacts to a refusal; it is not a test of
the real permission enforcement layer. That layer remains covered by
`internal/perm`, `internal/tools`, `internal/workspace`, and `internal/shell` tests.
Interrupted retry/no-duplicate-action behavior is covered by
`internal/session/retry_test.go` and `internal/tui/retry_test.go`.

Fixture scores validate the harness/session plumbing, **not model intelligence**.
Fixture latency is local execution overhead, not a provider benchmark. CI checks
timing accounting with an injected clock rather than asserting wall-clock speed.

## Opt-in live comparisons

```sh
go run ./cmd/yonder-eval --live --config /path/to/config.toml \
  --provider surplus --model qwen3-coder-next --timeout 30s --max-tokens 256
```

PowerShell uses the same flags on one line. Config keys continue to come from
environment variables. Explicit config, provider, and model are required.
Each case uses a fresh session, with fallback and automatic retries disabled.
This prevents another provider's answer from being attributed to the selected
model. Compatibility headers and `omit_stream_options` are honored.

A full normal run uses seven provider attempts: two one-round answer cases,
two two-round tool cases, and one cancellation probe. A model repeatedly calling
tools can reach the session's six-round bound per case, for up to 25 attempts.
The output limit applies per round. These calls may be billed. The development
runner does not update the normal CLI's daily request counter or saved sessions.

The report records suite version, UTC start time, provider/model, output cap,
timeout, retries, fallbacks, final finish reason, token usage when reported,
answers, and tool calls. Temperature is left at the provider default; it is not
controlled by this runner. Keep reports from repeated runs with the same flags
and suite version; compare each dimension independently rather than combining
correctness and speed into a single score. Provider/backend changes can still
affect reproducibility.

`first_token_ms: null` means no text arrived. A cancellation probe without a
first token is explicitly unobserved, never a zero-latency success. Cancelling
after the first token can coincide with an already-completed response, so this
is an observed unwind measurement, not proof that the remote provider stopped
generating or billing. Cancellation results are reported independently and do
not alter the answer/tool pass score.

Generated responses and tool arguments are untrusted text. Reports are JSON,
but may still contain sensitive material a provider returns. Store them locally
and inspect before sharing. Do not add private prompts to the built-in suite.

## Quality beyond the smoke suite

These four tasks are a small baseline, not a comprehensive model ranking. For
coding explanations and summaries, add versioned synthetic tasks plus a human
rubric covering correctness, relevance, instruction following, and incomplete
answers. Review outputs blind to model identity where practical. Record rubric
scores separately; keyword overlap alone is not a correctness measure. An
automated judge would add cost and its own bias and is not part of suite 1.

Live-provider execution is opt-in and is not part of CI. No live quality or
latency claims should be made from the offline fixture results.

## Budgeted live runs

Add `--budget-usd 0.10` to look up the selected model's current published rates
and reserve a conservative cost before every completion dispatch. Unknown or
invalid prices refuse the run. Input is estimated from serialized UTF-8 request
bytes plus a framing allowance; output reserves the entire `max_tokens` cap.
Reservations are never refunded. The guard also limits total dispatches to 25
and each request body to 64 KiB. Provider errors stop further cases and the
cancellation probe. Reports include rates, budget, and reserved amount.

This is a local token-cost guard, not a provider-side account spending cap.
Pricing changes, unreported fees, and a provider ignoring token limits cannot
be controlled locally. Leave a substantial margin below your spending limit.

### Live smoke comparison, 2026-09-20

Both Surplus `deepseek-v4-flash` and `openai-gpt-oss-120b` passed 4/4 cases,
using 1,024 output tokens maximum per round and 60-second case timeouts.
Each had a $0.10 guard, no fallback/retries, and seven dispatched requests
including the cancellation probe.

- DeepSeek: published input/output prices $0.09/$0.18 per million tokens.
  Case durations: 2.505s arithmetic, 6.991s JSON, 3.963s lookup, 13.234s denial.
  Conservative reserved cost: $0.0022149.
- GPT OSS: published input/output prices $0.07/$0.28 per million tokens.
  Case durations: 0.402s arithmetic, 0.619s JSON, 6.286s lookup, 1.652s denial.
  Conservative reserved cost: $0.00272692.

Combined reservation: $0.00494182. This is not a provider billing receipt.
Both cancellation measurements were below the clock's observed resolution
(reported 0 ms), not proof of remote generation stopping. These single runs
favor GPT OSS for interactive responsiveness on this suite and DeepSeek for
output-heavy token pricing; they cannot establish a universal best coding model.
The application default model was not changed by these evaluations.
