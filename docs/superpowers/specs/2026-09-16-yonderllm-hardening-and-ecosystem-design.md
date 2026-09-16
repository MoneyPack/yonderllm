# yonderllm Hardening and Ecosystem Design

## Goal
Execute the ten approved improvement areas in order while preserving yonderllm's core identity: a safe, provider-agnostic, single static binary that transports requests to remote inference providers and does not host a service.

## Constraints
- Go 1.27+ and Windows-compatible behavior remain supported.
- No local inference, model weights, GPU runtime, or required daemon.
- No always-on HTTP server. Existing `ask --json` / `run --json` remain the programmatic boundary.
- Credentials remain environment-backed and are never printed or persisted as plaintext.
- Chat mode remains incapable of filesystem or shell access.
- Tests must use deterministic local fixtures; no live provider calls in CI.

## Ordered milestones

1. **Terminal UX:** Add explicit stream phases and a compact activity indicator while waiting, preserve colored block types, and route provider/tool failures through actionable sanitized messages. The indicator must not alter headless output.
2. **Performance:** Add benchmarks and bounded-history/resource tests for SSE frames, transcript refresh, and session trimming. Optimize only measured allocations or unbounded growth; preserve readable code and cancellation semantics.
3. **Security:** Centralize error sanitization, strengthen recognized-secret redaction, validate config-file ownership/permissions where the platform exposes them, and add tests proving credentials do not appear in errors, snapshots, or diagnostics. MFA is out of scope because yonderllm owns no accounts.
4. **Providers/fallbacks:** Improve the generic OpenAI-compatible adapter and provider diagnostics first; add another adapter only if it is meaningfully distinct and testable with the existing contract. Make fallback eligibility explicit and include a concise reason in notices.
5. **Documentation:** Add focused guides for installation, provider setup, scripting, troubleshooting, recovery, safety modes, and development. Keep README as the quick path and link to deeper docs.
6. **Testing:** Add protocol fixtures, CLI end-to-end harness coverage, security regression tests, cancellation tests, and permission matrix cases. Keep live services out of CI.
7. **Configuration/extensions:** Add bounded retry/output/fallback settings with validation. Support provider-specific HTTP headers through config without storing secret values. Define an external command adapter contract only if useful; do not load arbitrary Go plugins or execute them implicitly.
8. **Ecosystem integration:** Improve stable NDJSON event schemas, exit codes, stdin composition, and examples for jq/PowerShell/Python. Defer a persistent HTTP API because it violates the no-service boundary.
9. **Community:** Add CONTRIBUTING, CODE_OF_CONDUCT, SECURITY, issue templates, and pull-request guidance with reproducible test expectations.
10. **Compatibility/versioning:** Add build metadata and a version command contract, document semantic compatibility and deprecation policy, update CI checks, run the full verification gate, commit each coherent milestone, and push.

## Component boundaries
- `internal/tui`: presentation-only activity and diagnostics state; no provider policy.
- `internal/provider`: wire parsing, HTTP behavior, error classification, and redaction-safe diagnostics.
- `internal/session`: retry/fallback/usage/history policy; no terminal rendering.
- `internal/config`: validated user configuration and precedence; no network calls.
- `internal/cli`: composition, stable machine-readable output, exit behavior, and build metadata.
- `docs/`: user/developer guidance and compatibility contracts.
- `.github/`: CI and community contribution surfaces.

## Data flow
CLI/TUI creates a validated config, resolves providers, creates a session, and consumes the same session event stream. TUI may render activity state; CLI maps events to prose or versioned NDJSON. Provider adapters classify transport/protocol errors. Session decides whether a fallback is safe, records usage, and never replays side-effecting tools during recovery.

## Error handling
Errors are typed internally, sanitized before presentation, and actionable where possible. Partial provider output prevents automatic fallback. Embedded provider error envelopes and incomplete streams are failures. User cancellations return interruption semantics and never trigger hidden retries.

## Verification
Every milestone has focused tests. Before completion run:

```text
git diff --check
go build ./...
gofmt -l internal cmd
go vet ./...
go test ./... -count=1
```
