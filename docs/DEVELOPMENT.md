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
prospective extension [threat model](security/workspace/kb/THREAT_MODEL.md)
records verified controls, working-tree fixes, and open follow-up checks.
