# yonderllm Hardening and Ecosystem Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Execute the ten approved improvements as ordered, testable milestones while preserving yonderllm’s safe single-binary remote-client boundary.

**Architecture:** Keep policy in session/provider/config packages and keep presentation in TUI/CLI. Extend the existing common event stream rather than creating parallel request paths. Use stable NDJSON and explicit external contracts instead of an always-on HTTP server or arbitrary in-process plugins.

**Tech Stack:** Go 1.27+, Bubble Tea, Lip Gloss, Cobra, TOML, `go test`, `go vet`, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-09-16-yonderllm-hardening-and-ecosystem-design.md`

## Global Constraints

- No local inference, weights, GPU runtime, or required daemon.
- No always-on HTTP server; `ask --json` and `run --json` are the programmatic boundary.
- Credentials remain environment-backed and are never printed or persisted as plaintext.
- Chat mode has no filesystem or shell capability.
- CI uses deterministic local fixtures and never live provider calls.
- Run `git diff --check && go build ./... && gofmt -l internal cmd && go vet ./... && go test ./... -count=1` before completion.

---

### Task 1: Terminal UX and diagnostics

**Files:**
- Modify: `internal/tui/model.go`, `internal/tui/update.go`, `internal/tui/view.go`, `internal/tui/styles.go`
- Modify: `internal/provider/errors.go`, `internal/session/session.go`
- Test: `internal/tui/edges_test.go`, `internal/tui/tui_test.go`, provider/session error tests

**Interfaces:**
- Produce an idle/connecting/streaming/tool-running status rendered only by TUI.
- Preserve CLI/headless event behavior.

- [ ] Write tests asserting each status, spinner/frame update, and actionable sanitized error.
- [ ] Run focused TUI/provider/session tests and observe failures.
- [ ] Implement the smallest state and rendering changes; keep status updates cancellable.
- [ ] Run focused tests and `gofmt`.
- [ ] Commit: `Improve terminal activity and error guidance`.

### Task 2: Measure and optimize resource usage

**Files:**
- Modify: `internal/provider/chatcompat_test.go`, `internal/session/history_test.go`, `internal/tui/*_test.go`
- Add: `internal/provider/benchmark_test.go`, `internal/session/benchmark_test.go`
- Modify only if measurements justify it: `internal/provider/chatcompat.go`, `internal/session/history.go`, `internal/tui/model.go`

- [ ] Add benchmarks for representative SSE frames, trimming, and transcript refresh.
- [ ] Run `go test ./internal/provider ./internal/session ./internal/tui -bench . -benchmem` and record baseline.
- [ ] Add bounded-input tests for 1 MiB frames, long history, and cancellation.
- [ ] Optimize the measured hotspot without changing public behavior.
- [ ] Re-run benchmarks/tests and commit: `Bound session and stream resource use`.

### Task 3: Credential and diagnostic hardening

**Files:**
- Modify: `internal/config/config.go`, `internal/provider/errors.go`, `internal/session/sessions.go`
- Test: `internal/config/*_test.go`, `internal/provider/*_test.go`, `internal/session/sessions_test.go`

- [ ] Add failing tests for key-shaped values in nested errors, snapshots, and provider diagnostics.
- [ ] Add config permission/ownership validation with platform-safe behavior and clear remediation.
- [ ] Centralize sanitization and apply it before user-visible error construction.
- [ ] Verify no secret values appear in serialized config/snapshots/errors.
- [ ] Run focused security tests and commit: `Harden credential and error handling`.

### Task 4: Provider coverage and fallback diagnostics

**Files:**
- Modify: `internal/provider/chatcompat.go`, `internal/provider/errors.go`, `internal/session/session.go`
- Test: `internal/provider/chatcompat_test.go`, `internal/session/session_test.go`
- Docs: `README.md`, `SPEC.md`

- [ ] Add fixture tests for timeout, 401/403, 402/429, 5xx, malformed JSON, embedded errors, and incomplete streams.
- [ ] Define and test fallback eligibility for each typed error and partial output.
- [ ] Improve fallback notices with provider and reason while keeping secrets out.
- [ ] Add a distinct adapter only if its wire contract cannot be represented by ChatCompat; otherwise document supported compatibility.
- [ ] Run focused tests and commit: `Clarify provider fallback behavior`.

### Task 5: Documentation guides

**Files:**
- Add: `docs/INSTALL.md`, `docs/PROVIDERS.md`, `docs/TROUBLESHOOTING.md`, `docs/SCRIPTING.md`, `docs/SAFETY.md`, `docs/DEVELOPMENT.md`
- Modify: `README.md`, `SPEC.md`

- [ ] Write commands matching the actual CLI flags and paths.
- [ ] Include provider setup, no-key errors, retry recovery, permission modes, and NDJSON examples.
- [ ] Add links from README and verify every local link target exists.
- [ ] Commit: `Document setup scripting and troubleshooting`.

### Task 6: Meaningful integration and edge coverage

**Files:**
- Add: `internal/integration/*_test.go` or package-local fixture tests where package boundaries require it.
- Modify: `internal/cli/*_test.go`, `internal/tools/*_test.go`, `internal/perm/*_test.go`

- [ ] Add deterministic HTTP-server integration flows for fallback and streaming errors.
- [ ] Add CLI stdin/NDJSON/exit-code tests and permission matrix regression cases.
- [ ] Add cancellation and save/resume boundary tests.
- [ ] Run all tests with `-count=1`; commit: `Cover end to end failure paths`.

### Task 7: Configuration and scoped extensions

**Files:**
- Modify: `internal/config/config.go`, `internal/cli/cli.go`, provider construction files
- Test: `internal/config/*_test.go`, `internal/cli/*_test.go`
- Docs: `docs/PROVIDERS.md`, `SPEC.md`

- [ ] Add failing tests for configured retry count/backoff, output format, and provider-specific non-secret headers.
- [ ] Add typed config fields, precedence, validation, and bounded retry behavior.
- [ ] Keep arbitrary Go plugins and implicit command execution out of the binary; document the stable provider configuration seam.
- [ ] Run focused tests and commit: `Add bounded runtime configuration`.

### Task 8: CLI ecosystem integration

**Files:**
- Modify: `internal/cli/run.go`, `internal/cli/ask.go`, `internal/cli/help.go`
- Test: `internal/cli/run_test.go`, `internal/cli/ask_test.go`
- Docs: `docs/SCRIPTING.md`, `README.md`

- [ ] Define and test versioned NDJSON event fields for delta, tool, notice, done, and error.
- [ ] Preserve stdout purity in JSON mode and map interruption/provider errors to stable exit codes.
- [ ] Add jq, PowerShell, and Python pipeline examples.
- [ ] Run CLI tests and commit: `Stabilize scriptable CLI output`.

### Task 9: Community contribution surfaces

**Files:**
- Add: `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `SECURITY.md`, `.github/ISSUE_TEMPLATE/bug_report.md`, `.github/ISSUE_TEMPLATE/feature_request.md`, `.github/pull_request_template.md`
- Modify: `README.md`

- [ ] Write reproducible bug-report fields and safe disclosure instructions.
- [ ] Document local verification commands and no-live-provider CI expectations.
- [ ] Link contribution and security guidance from README.
- [ ] Verify template paths and commit: `Add contribution and support guidance`.

### Task 10: Versioning and final verification

**Files:**
- Modify: `internal/cli/cli.go`, `cmd/yonderllm/main.go`, `.github/workflows/ci.yml`, `README.md`, `SPEC.md`
- Add if needed: `internal/cli/version_test.go`, `docs/COMPATIBILITY.md`

- [ ] Add tests for `--version`, default dev version, and link-time build metadata contract.
- [ ] Document semantic versioning, stable CLI/NDJSON guarantees, and deprecation policy.
- [ ] Ensure CI runs formatting, vet, build, and tests.
- [ ] Run the complete verification gate.
- [ ] Inspect `git status`, `git diff`, and recent log; stage only intended files.
- [ ] Commit: `Define compatibility and release policy` and push.
