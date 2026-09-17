# Compatibility and releases

`yonderllm --version` keeps its concise output. `yonderllm version --json` reports
version, optional commit/build date, dirty-build status, Go version, platform and
NDJSON schema version. It works without provider credentials or a valid config.

Source builds default to `dev`. Release builders stamp `Version`, `BuildCommit`
and `BuildDate` using Go linker flags:

```sh
go build -ldflags "-s -w -X yonderllm/internal/cli.Version=0.2.0 -X yonderllm/internal/cli.BuildCommit=COMMIT -X yonderllm/internal/cli.BuildDate=UTC_DATE" -o bin/yonderllm ./cmd/yonderllm
```

Substitute the actual release version, commit and UTC date. This example does not
announce a published release. Go's VCS build settings provide metadata when linker
values are absent.

## Version policy

Use semantic versioning for releases. During 0.x, a minor version may change
contracts with release notes. At 1.x, incompatible public flag/schema/config
changes require a major version. Patch releases fix defects without intentionally
changing contracts. Provider/model availability is controlled by third parties.

NDJSON schema 1 adds `schema_version: 1` to each event. Consumers must tolerate
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
