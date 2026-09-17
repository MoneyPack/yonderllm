# Contributing

Report reproducible bugs or propose focused improvements at
https://github.com/MoneyPack/yonderllm/issues.

Build with Go 1.27 or newer. Before submitting:

```sh
gofmt -w internal cmd
git diff --check
go build ./...
go vet ./...
go test ./... -count=1
```

Add regression tests for changed behavior using local HTTP fixtures. Do not call
live providers in tests or commit credentials/conversation snapshots. Include
before/after benchmark results for performance claims. Keep provider wire logic,
session policy, and CLI/TUI rendering separate. Permission changes must cover
denial and noninteractive behavior.

Submit a focused pull request explaining the user problem, behavior, checks run,
and any compatibility implications. Maintainers review contributions as time
allows. See [development](docs/DEVELOPMENT.md) and [conduct](CODE_OF_CONDUCT.md).
