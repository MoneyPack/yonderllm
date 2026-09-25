# Changelog

All notable changes to yonderllm are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html); during 0.x a minor
release may change contracts, as described in
[docs/COMPATIBILITY.md](docs/COMPATIBILITY.md).

## [Unreleased]

Working-tree changes since v0.2.0-rc.2. Not yet published as a release.

### Added

- Per-provider `context_window` TOML key (a positive token count). When set,
  conversation history is trimmed to the model's real window instead of the
  conservative 8192-token default; `config show --json` and `models --json`
  report it.
- `run --json` events now carry the `model` of the provider that produced them,
  alongside `provider`, so a line after a fallback is self-describing.
- Approval prompts for agent-mode reads of credential-like files (`.env*`,
  `id_rsa*`, `*.pem`, `.netrc`, `.aws/credentials`, …); code mode refuses them.
- `internal/adapters` (one provider-config → adapter factory) and
  `internal/redact` (shared credential-pattern redaction) packages;
  `session.Options` decouples the session from `config.Config`.
- Tag-triggered release workflow (`.github/workflows/release.yml`): re-runs
  tests and `govulncheck`, cross-compiles the six targets with the version,
  commit and date stamped in, and publishes them with `SHA256SUMS.txt`.
  Pre-release tags (`-rc.N`) are marked as such.
- CI now also runs `git diff --check`, `golangci-lint`, `govulncheck`, arm64
  cross-builds and a benchmark compile smoke; Dependabot for Go modules and
  Actions.
- `go install github.com/MoneyPack/yonderllm/cmd/yonderllm@latest` works, and
  a binary installed that way reports its module version instead of `dev`.
- `CHANGELOG.md`; a supported-platforms table; `docs/THREAT_MODEL.md`
  (moved from `docs/security/workspace/kb/`) pinned to its baseline commit;
  `docs/history/` for retired planning records.
- Regression tests for TUI key routing (typing no longer scrolls the
  transcript), the approval double-answer race, and rendering.

### Changed

- **Module path is now `github.com/MoneyPack/yonderllm`** (was the bare
  `yonderllm`). Linker flags that stamp the version must use the new path:
  `-X github.com/MoneyPack/yonderllm/internal/cli.Version=...`. No effect on
  the binary's flags, config or output.
- **`--yes` no longer waives confirmation** of shells, script interpreters and
  launchers (`sh`, `bash`, `cmd`, `powershell`, `python*`, `node`, `xargs`,
  `env`, `sudo`, `find -exec`, `git -c`, …), of force-style flags, or of
  writes to hook-like files. Windows launchable extensions are stripped before
  matching, so `rm.cmd` is `rm`. `docs/SAFETY.md` now says "recognised"
  destructive commands rather than claiming an unconditional guarantee.
- `/mode` in the TUI rebuilds the tool set from the mode name alone, so `--yes`
  is dropped after a mode switch (this was already the behaviour; it is now
  documented).
- TUI: keys are routed to the input only, so `j`/`k`/space while typing no
  longer scroll the transcript; rendered blocks are cached and the view jumps
  to the bottom on new output only if it was already there; `/read`, `/search`
  and `/save` run as commands with a busy notice instead of blocking `Update`.
- `Workspace.Search` honours cancellation (ctrl+c stops a tree walk).
- README shrunk to install, quick start, command/flag summary, TUI keys, a
  modes summary and links; the detailed Tools/Modes/containment material moved
  to `docs/SAFETY.md`, the event schema to `docs/SCRIPTING.md`, CI /
  cross-compiling / layout / conventions to `docs/DEVELOPMENT.md`, release
  builds and completion to `docs/INSTALL.md`, provider defaults and price tiers
  to `docs/PROVIDERS.md`.
- `SPEC.md` §3 layout lists every package; §4 describes `run` as the headless
  command (TUI only with no prompt at a terminal) and lists every subcommand
  and persistent flag; §8 documents `context_window`.

### Fixed

- `run --json` stamped the *active* provider's model on every event, so after
  a fallback the `model` field named a model that had not answered.
- Context-window trimming was dead code: `SetContextWindow` was only called
  from tests, so every model got the 8192-token budget.
- TUI approval race: a second keystroke while a question was pending could
  issue a second answer that rewrote an allowed call as denied; a question
  still open when the exchange ends (timeout, cancel) is now resolved as
  "denied (exchange ended)" instead of keeping the keyboard.
- README claimed "write and execute are not built yet" although `write_file`
  and `run_command` shipped in v0.2.0-rc.1; flags/commands tables, dependency
  count (eight, not five), release links (rc.2), and project layout corrected.
- CodeRabbit badge pointed at the wrong repository owner; LICENSE copyright
  holder spelling corrected to the GitHub owner (`MoneyPack`).
- `sessions` lock helper produced a `"lock sessions: <nil>"` error; the flock
  helper is now shared with the usage store.

### Security

- Child processes started by `run_command` no longer inherit the environment
  variables the configuration names as credentials (`api_key_env`,
  `header_env`) or a short well-known list (`OPENAI_API_KEY`, `GITHUB_TOKEN`,
  `AWS_SECRET_ACCESS_KEY`, …), matched case-insensitively. Previously `env`,
  `printenv` or `go env` under `--yes` could hand every key to the provider
  and the autosaved transcript.
- Writes under `.git/` are refused in every mode. Writes under any
  dot-directory, to dotfiles, and to build/manifest/tool-configuration files
  (`Makefile`, `package.json`, `go.mod`, `*.ps1`, `docker-compose*.yml`, …)
  are confirmed even under `--yes`, closing the "write a hook, then trigger it
  with a harmless command" chain.
- Reads under `.git/` and the other directories search skips (`node_modules`,
  `vendor`, `bin`, `dist`, `build`, `target`, …) are refused.

## [0.2.0-rc.2] - 2026-09-21

Pre-release. Safer approvals, a clearer terminal interface, and a versioned
provider evaluation suite.

### Added

- `cmd/yonder-eval`, a development-only provider evaluation runner: offline
  deterministic fixtures by default, opt-in live comparisons with
  `--live --provider --model`, and a per-run token-cost guard
  (`--budget-usd`). Suite 2 scores the final assistant turn and reports tool
  requests ignored at the round limit. See `docs/EVALUATION.md`.
- `docs/SAFETY.md` documenting workspace boundaries, approval behaviour, and
  known limitations; a working-tree threat model and architecture notes.
- `internal/terminaltext`: terminal control bytes in provider text, approval
  targets, labels and setup errors are rendered as visible escapes.

### Changed

- The welcome screen fits small terminals and yields to conversation activity.
- Input entered while a cancellation is finishing is preserved and released
  once the previous worker exits, instead of racing it.
- Workspace reads and searches cap actual bytes consumed (not just the stat
  size), refuse known special files, and re-check the opened handle type.
- Commands get a one-second output-drain grace period (`exec.Cmd.WaitDelay`)
  after the direct child exits or is cancelled.
- Model-catalogue responses are bounded at 16 MiB and rejected before decoding.

### Fixed

- Approval requests hidden by an undersized terminal can no longer be
  accepted; enlarge the terminal to approve, or deny.
- File writes detect concurrent edits even when large changes produce
  identical diff summaries (a full bounded snapshot is compared, not the
  summary).
- Pricing lookups honour configured headers and escape terminal controls in
  errors.

## [0.2.0-rc.1] - 2026-09-18

Pre-release. The first candidate with the model's tools, approvals, saved
conversations, and a stable scripting contract.

### Added

- Tools for the model: `read_file`, `search_files`, `write_file`, and
  `run_command`, gated by the permission policy and offered only when the mode
  allows them. Writes show a diff and commands show the exact argument vector
  before you approve; denial is the default and is reported back to the model.
- `--yes` to accept agent-mode writes and commands in advance (agent mode
  only); a bounded six-round tool loop that withholds tools on the last round.
- Saved conversations: autosave after each exchange, `/save <name>`,
  `--resume <name>`, `--last`, `--save <name>`, `--no-save`, and the
  `sessions` / `sessions delete` commands. Snapshots are versioned JSON with
  best-effort secret redaction.
- `/mode [chat|code|agent]` to switch permission mode without leaving the TUI,
  and `/retry` to resend an interrupted exchange with tools disabled.
- `run --json` schema 1: `schema_version` on every event, `tool` /
  `tool_result` events paired by `id`, and `version --json` build metadata.
  `--stdin` appends piped input to a prompt argument.
- Persistent daily request cap shared across processes (`usage.json`, locked
  read/modify/write, resets at local midnight).
- Runtime settings `retry_attempts`, `retry_backoff_ms`,
  `request_timeout_seconds`, `output_format`; per-provider
  `omit_stream_options` and `header_env`.
- Guides under `docs/` (install, providers, troubleshooting, scripting,
  safety, development, compatibility), `CONTRIBUTING.md`, `SECURITY.md`,
  `CODE_OF_CONDUCT.md`, issue and pull-request templates.
- CI matrix on Ubuntu, Windows and macOS plus a Linux race/coverage job.

### Changed

- Gemini is reached through its OpenAI-compatible endpoint; every provider has
  a default model and providers without one are skipped.
- A provider's 5xx, quota, authentication or missing-model failure moves to
  the next provider before any text arrives; partial text stops fallback.
- Obvious secrets are redacted before a message leaves the machine; exact
  configured keys are redacted from provider errors.
- Provider endpoints must be HTTP(S) without userinfo, query or fragment;
  redirects are refused. Unix config files must be owner-only (0600).
- Streams are bounded: 4 MiB prompt, 1 MiB per SSE line, 16 MiB per provider
  round. Error frames inside HTTP 200 streams and EOF without a finish marker
  are reported as errors.
- An explicit notice explains when a reply hit the provider's output-token
  limit.
- TUI phase/activity feedback and actionable typed-error guidance.

## [0.1.0] - 2026-09-10

First release.

### Added

- A terminal client for remote language models: one static binary, no local
  inference, Windows/Linux/macOS builds for amd64 and arm64.
- Four providers speaking the same chat-completions adapter: Groq (default),
  Gemini, OpenRouter, and Surplus. Any OpenAI-compatible endpoint can be added
  in the TOML config.
- Price tiers: every model is banded `free`, `cheap`, `paid`, or `unknown`
  from the provider's published pricing; `models` shows free and cheap only,
  `--all` lifts the filter.
- The Bubble Tea TUI with `/model`, `/clear`, `/read`, `/search`, `/usage`,
  `/help`; the `ask`, `run`, `models`, `providers`, `config`, and `completion`
  commands.
- Permission modes `chat`, `code`, `agent`, with `/read` and `/search` gated
  by the policy; a daily request cap.
- Configuration resolved from defaults → TOML file → `YONDERLLM_*` environment
  → flags. Keys are named by environment variable and never stored.

### Known gaps at release

- `internal/workspace` was a placeholder and the permission policy's `Check`
  was not yet wired into a caller outside its package; both landed in
  v0.2.0-rc.1.

[Unreleased]: https://github.com/MoneyPack/yonderllm/compare/v0.2.0-rc.2...HEAD
[0.2.0-rc.2]: https://github.com/MoneyPack/yonderllm/compare/v0.2.0-rc.1...v0.2.0-rc.2
[0.2.0-rc.1]: https://github.com/MoneyPack/yonderllm/compare/v0.1.0...v0.2.0-rc.1
[0.1.0]: https://github.com/MoneyPack/yonderllm/releases/tag/v0.1.0
