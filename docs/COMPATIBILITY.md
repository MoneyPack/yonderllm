# Compatibility and releases

`yonderllm --version` keeps its concise output. `yonderllm version --json` reports
version, optional commit/build date, dirty-build status, Go version, platform and
NDJSON schema version. It works without provider credentials or a valid config.

Source builds default to `dev`. Release builders stamp `Version`, `BuildCommit`
and `BuildDate` using Go linker flags:

```sh
mkdir -p bin
go build -ldflags "-s -w -X github.com/MoneyPack/yonderllm/internal/cli.Version=0.2.0 -X github.com/MoneyPack/yonderllm/internal/cli.BuildCommit=COMMIT -X github.com/MoneyPack/yonderllm/internal/cli.BuildDate=UTC_DATE" -o bin/yonderllm ./cmd/yonderllm
```

Substitute the actual release version, commit and UTC date. This example does not
announce a published release. Go's VCS build settings provide metadata when linker
values are absent, and a binary from `go install .../cmd/yonderllm@vX.Y.Z` reports
the module version Go embedded rather than `dev`.

Published releases are produced by `.github/workflows/release.yml` when a `v*`
tag is pushed: it re-runs the test suite and `govulncheck`, cross-compiles the
six supported targets with `CGO_ENABLED=0 -trimpath` and the flags above, and
uploads the binaries with a `SHA256SUMS.txt` to a GitHub release. A tag with a
pre-release suffix (`v0.2.0-rc.3`) is marked as a pre-release.

## Version policy

Use semantic versioning for releases. During 0.x, a minor version may change
contracts with release notes. At 1.x, incompatible public flag/schema/config
changes require a major version. Patch releases fix defects without intentionally
changing contracts. Provider/model availability is controlled by third parties.

NDJSON schema 1 adds `schema_version: 1` to each event. Every event also
carries `provider` and `model`, naming the provider and model that produced
that event (the fallback's after a fallback, not the one originally asked
for); this was corrected within schema 1 because it changed a value, not a
field's type or presence. Consumers must tolerate
unknown fields/event types and require `done` for success after streaming starts.
`error` is terminal; process exit status remains authoritative. A field removal,
type change, or changed meaning requires a new schema version. Plain TUI/prose
formatting is not a parsing contract. Exit codes remain 0, 1 and 130.

Saved conversations use their own versioned schema. Unsupported versions are
rejected rather than interpreted heuristically. Permission/approval state is
never restored. Config keys added here have backward-compatible defaults.

Deprecations should name the replacement in help/docs and release notes, remain
available for at least one minor release, and avoid notices on NDJSON stdout.
Security defects may require immediate restrictions; document them explicitly.

## Current intentional restrictions

- Unix config permissions must be owner-only. Use `chmod 600`.
- Provider endpoints reject URL credentials, query parameters and fragments.
- Provider redirects are refused; configure the final API endpoint.
- Bare EOF without a completion marker is an interrupted stream.

Tests run on Windows, Linux and macOS in CI. The local Windows verification does
not by itself establish a green remote CI run.
