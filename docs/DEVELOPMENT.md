# Development

The module uses Go 1.27. Run from the repository root:

```sh
git diff --check
go build ./...
gofmt -l internal cmd
go vet ./...
go test ./... -count=1
```

The formatter must list no paths. Tests use local HTTP fixtures and temporary
config/cache directories. Never make live provider calls in CI.

`go test ./internal/session ./internal/provider -bench . -benchmem -run '^$'`
measures the history and parser paths. On Windows/amd64, the 1,000-message
prompt benchmark changed from 1,428,481 B/op and 1,003 allocations to 148,480 B/op
and 3 allocations after lazy redaction copying. Timing is machine-dependent.

Use `go test -race ./...` where CGO and a C compiler are available. The Ubuntu
CI job performs race checks; local CGO-disabled runs cannot substitute for it.

Package boundaries: provider handles wire protocols, session handles policy and
history, config handles settings, tools/perm handle capabilities, and CLI/TUI
handle presentation. Preserve cancellation and whole tool-result exchanges.

The separate [evaluation runner](EVALUATION.md) uses offline fixtures by default;
live model comparisons are explicit and excluded from CI. The current and
prospective extension [threat model](THREAT_MODEL.md)
records verified controls, working-tree fixes, and open follow-up checks.

On Windows, if Go is installed but not on `PATH`:

```powershell
& "C:\Program Files\Go\bin\go.exe" vet ./...
& "C:\Program Files\Go\bin\go.exe" test ./...
```

## Project layout

```
cmd/yonderllm/         entry point; nothing but wiring
cmd/yonder-eval/       development-only provider evaluation runner (see EVALUATION.md)
internal/cli/          commands, flags, help text, and the defaults → config → env → flags resolver
internal/config/       the config file, its paths, validation, and precedence rules
internal/adapters/     the single factory turning provider config into a provider.Adapter
internal/provider/     HTTP clients for OpenAI-compatible endpoints (SSE parsing, model catalogue)
internal/session/      conversation state, streaming, fallback chain, tool rounds, context trimming,
                       saved conversations, and the persistent daily-cap counter
internal/perm/         the permission policy: modes, actions, allow/ask/deny, auto-approval
internal/tools/        the capabilities the model is offered, gated by that policy, with approvals
internal/workspace/    os.Root-confined reads, searches, and writes; credential/hook name classification
internal/shell/        argv-only command execution, destructive/interpreter tables, env scrubbing
internal/redact/       credential-pattern redaction shared by provider errors and saved history
internal/terminaltext/ escaping of terminal control bytes for TUI and CLI output
internal/evaluation/   the offline/live evaluation suite behind cmd/yonder-eval
internal/tui/          the Bubble Tea interface
```

Built on [Cobra](https://github.com/spf13/cobra),
[Bubble Tea](https://github.com/charmbracelet/bubbletea),
[Bubbles](https://github.com/charmbracelet/bubbles),
[Lip Gloss](https://github.com/charmbracelet/lipgloss),
[charmbracelet/x/ansi](https://github.com/charmbracelet/x) and
[x/term](https://github.com/charmbracelet/x),
[gofrs/flock](https://github.com/gofrs/flock), and
[BurntSushi/toml](https://github.com/BurntSushi/toml). Eight direct
dependencies (see `go.mod`), no cgo, one binary.

## Continuous integration

Every push to `main` or an `improve/**` branch, and every pull request against
`main`, runs [`.github/workflows/ci.yml`](../.github/workflows/ci.yml). The
toolchain version comes from `go.mod` (`go-version-file`), so the workflow
cannot drift from what the module declares. Superseded runs on the same branch
are cancelled.

Five jobs:

- **test** — gofmt (the file list is the signal; gofmt itself always exits 0),
  `go vet`, `go build`, and `go test ./... -count=1` on `ubuntu-latest`,
  `windows-latest`, and `macos-latest`, the three platforms the project ships
  binaries for. The matrix does not fail fast, so one red platform still
  reports the others. It earns its cost: reserved device names are refused on
  Windows only, and the configuration search path differs on every OS.
- **race and coverage** — `go test -race -coverprofile` plus a coverage total,
  on Ubuntu alone. The race detector requires cgo, and yonderllm is
  deliberately pure Go, so a C compiler is not something every runner can be
  assumed to have. One platform is enough: the goroutines under test are the
  session's tool loop and event stream, which are identical everywhere.
- **lint** — `git diff --check` over tracked files and `golangci-lint`
  (pinned version, configured by [`.golangci.yml`](../.golangci.yml)). Run it
  locally without installing anything:
  `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run`.
- **govulncheck** — reports known vulnerabilities reachable from the code, not
  merely present in the dependency graph.
- **cross-build and benchmarks** — `GOARCH=arm64` builds for linux and darwin,
  so a portability break is caught before release time, and every benchmark
  compiled and run once (`-run '^$' -bench . -benchtime=1x`) so they cannot rot.

CI runtime-tests amd64 only. arm64 binaries are cross-compiled and are not
executed in CI; see the supported-platforms table in the
[README](../README.md#supported-platforms).

Dependabot ([`.github/dependabot.yml`](../.github/dependabot.yml)) opens
weekly grouped pull requests for Go modules and GitHub Actions.

## Releasing

Pushing a `v*` tag runs [`.github/workflows/release.yml`](../.github/workflows/release.yml):

1. **verify** re-runs `go vet`, the test suite and `govulncheck` on the tagged
   commit, so a tag can never publish something CI has not passed.
2. **build** cross-compiles the six targets with `CGO_ENABLED=0 -trimpath` and
   stamps `Version`, `BuildCommit` and `BuildDate` (see
   [compatibility](COMPATIBILITY.md)); the linux/amd64 binary is executed once
   to confirm `version --json` reports the tag.
3. **publish** writes `SHA256SUMS.txt` and creates the GitHub release with the
   binaries attached. Tags with a suffix (`v0.2.0-rc.3`) become pre-releases.

To cut a release: move the *Unreleased* section of `CHANGELOG.md` under the new
version, commit, then `git tag -a vX.Y.Z -m "yonderllm vX.Y.Z"` and
`git push origin vX.Y.Z`. Because the module path is
`github.com/MoneyPack/yonderllm`, the same tag also makes
`go install github.com/MoneyPack/yonderllm/cmd/yonderllm@vX.Y.Z` work.

## Cross-compiling

```sh
GOOS=linux   GOARCH=amd64 go build -o dist/yonderllm-linux-amd64        ./cmd/yonderllm
GOOS=linux   GOARCH=arm64 go build -o dist/yonderllm-linux-arm64        ./cmd/yonderllm
GOOS=darwin  GOARCH=amd64 go build -o dist/yonderllm-darwin-amd64       ./cmd/yonderllm
GOOS=darwin  GOARCH=arm64 go build -o dist/yonderllm-darwin-arm64       ./cmd/yonderllm
GOOS=windows GOARCH=amd64 go build -o dist/yonderllm-windows-amd64.exe  ./cmd/yonderllm
GOOS=windows GOARCH=arm64 go build -o dist/yonderllm-windows-arm64.exe  ./cmd/yonderllm
```

No cgo, so every target cross-compiles from any host. Release binaries are
built with `CGO_ENABLED=0 -trimpath` and the version stamped in with the
linker flags described in [compatibility](COMPATIBILITY.md), with a
`SHA256SUMS.txt` beside them. The [release workflow](#releasing) does all of
this from a tag; the commands above are for local checks.

## Conventions

- Every file opens with a prose doc comment explaining what it is for and why
  it is separate from its neighbours.
- Tests assert on substrings, not exact output, so that wording can improve
  without a test rewrite.
- Tests are deterministic and never touch the network; provider behaviour is
  driven by local HTTP fixtures.
- `SPEC.md` holds the goals, non-goals, and interface contracts. It is the
  document to change first when the design changes; the README and `docs/`
  follow it.
- User-visible behaviour changes go in `CHANGELOG.md` under *Unreleased*.
