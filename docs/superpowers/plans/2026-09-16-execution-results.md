# Execution results

All ten approved areas were addressed on `improve/hardening-ecosystem`.

1. TUI phase/activity feedback; actionable typed-error guidance.
2. History allocation benchmarks and lazy copying; nested history isolation.
   Final review added 4 MiB CLI prompt and 16 MiB aggregate stream bounds.
3. Exact-key redaction, redirect refusal, endpoint validation, Unix owner-only
   config permission check. Windows retains ACL-based behavior.
4. Categorized fallback notices and strict-server stream-options compatibility.
   Generic adapter extension selected over another duplicate provider wrapper.
5. Six linked installation/provider/troubleshooting/scripting/safety/development guides.
6. End-to-end malformed/truncated/oversized stream tests and activity lifecycle tests.
7. Bounded opt-in retries/backoff, timeout, run output setting, environment-backed
   metadata headers; fixed explicit zero daily cap. Provider configuration is the
   supported extension seam; no arbitrary in-process plugin loader.
8. Schema-versioned NDJSON, output-write failures, explicit piped context via
   `--stdin`, and subprocess examples. No HTTP daemon, as approved.
9. Contribution/conduct/security guidance, issue templates, PR template.
10. Version/build JSON, compatibility/deprecation policy, CI branch coverage.

## Evidence

Windows/amd64 local checks passed: `git diff --check`, `go build ./...`,
`gofmt -l internal cmd` (no paths), `go vet ./...`,
`go test ./... -count=1` (all ten packages).

`go run ./cmd/yonderllm version --json` reports dev, Go 1.27.0, windows/amd64,
schema 1 without provider credentials.

BenchmarkHistoryPrompt: 1,428,481 -> 148,480 B/op; 1,003 -> 3 allocations.
BenchmarkHistoryMessages: 1,004 -> 2 allocations. Timing is host-specific.

Local CGO_ENABLED=0 prevents race testing. Ubuntu CI remains responsible for
race checks; Linux/macOS runtime checks and remote CI results were not verified
locally. Unix config-permission test is skipped on Windows.

## Scope decisions and limitations

MFA is provider-managed; no accounts/server subsystem was added. No new provider
pricing/model availability claims were made. Redaction remains best effort.
Existing transcript history can still grow with session duration; only prompt
input and individual provider stream buffering received explicit byte limits.
The source plan is retained as the planning record; this document records actual
deliverables rather than asserting every exploratory plan step was performed.
