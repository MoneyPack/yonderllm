# Installation

## Prebuilt binaries

Every release on the [releases page](https://github.com/MoneyPack/yonderllm/releases)
ships one standalone executable per platform plus a `SHA256SUMS.txt`. No Go
installation, model weights, or inference runtime is needed. The current
preview is
**[v0.2.0-rc.2](https://github.com/MoneyPack/yonderllm/releases/tag/v0.2.0-rc.2)**;
the last stable release is [v0.1.0](https://github.com/MoneyPack/yonderllm/releases/tag/v0.1.0).

| File | Platform | Tested |
| --- | --- | --- |
| `yonderllm-linux-amd64` | Linux, x86-64 | CI (`ubuntu-latest`) |
| `yonderllm-windows-amd64.exe` | Windows, x86-64 | CI (`windows-latest`) |
| `yonderllm-darwin-amd64` | macOS, Intel | CI (`macos-latest`) |
| `yonderllm-linux-arm64` | Linux, ARM64 | cross-compiled only |
| `yonderllm-darwin-arm64` | macOS, Apple silicon | cross-compiled only |
| `yonderllm-windows-arm64.exe` | Windows, ARM64 | cross-compiled only |

Download the binary and `SHA256SUMS.txt`, verify, then put the binary on PATH:

```powershell
# Windows
Get-FileHash .\yonderllm-windows-amd64.exe -Algorithm SHA256   # compare with SHA256SUMS.txt
Rename-Item .\yonderllm-windows-amd64.exe yonderllm.exe
```

```sh
# Linux
sha256sum -c SHA256SUMS.txt --ignore-missing
chmod +x yonderllm-linux-amd64
mv yonderllm-linux-amd64 /usr/local/bin/yonderllm

# macOS
shasum -a 256 yonderllm-darwin-arm64                          # compare with SHA256SUMS.txt
chmod +x yonderllm-darwin-arm64
mv yonderllm-darwin-arm64 /usr/local/bin/yonderllm
```

Binaries are built with `CGO_ENABLED=0 -trimpath`. Windows binaries are not
Authenticode signed and macOS binaries are not notarized, so the OS may ask you
to confirm the first launch.

## From source

Requires Go 1.27 or newer.

```sh
git clone https://github.com/MoneyPack/yonderllm.git
cd yonderllm
mkdir -p bin
go build -o bin/yonderllm ./cmd/yonderllm
./bin/yonderllm --version
```

On Windows PowerShell use `New-Item -ItemType Directory -Force .\bin` first,
then `go build -o bin/yonderllm.exe ./cmd/yonderllm`.
Put the executable on PATH. No model weights or inference runtime are installed.

### `go install`

The module path is `github.com/MoneyPack/yonderllm`, so a Go toolchain can
fetch and build a tagged release directly into `$GOPATH/bin` (usually
`~/go/bin`):

```sh
go install github.com/MoneyPack/yonderllm/cmd/yonderllm@latest
```

Binaries installed this way report the module version (for example `v0.2.0`)
from Go's embedded build info rather than `dev`, but carry no `BuildCommit` or
`BuildDate`; the release binaries do.

### Release build

Stripped, with the version stamped in (source builds otherwise report `dev`):

```sh
go build -ldflags "-s -w -X github.com/MoneyPack/yonderllm/internal/cli.Version=0.2.0" \
  -o bin/yonderllm ./cmd/yonderllm
```

On Windows PowerShell:

```powershell
New-Item -ItemType Directory -Force .\bin | Out-Null
go build -ldflags "-s -w -X github.com/MoneyPack/yonderllm/internal/cli.Version=0.2.0" `
  -o .\bin\yonderllm.exe .\cmd\yonderllm
```

Tagged releases are built by the `release` workflow in
[`.github/workflows/release.yml`](../.github/workflows/release.yml), which
stamps all three values and publishes `SHA256SUMS.txt` beside the binaries.

Verify with `yonderllm --version` (concise) or `yonderllm version --json`
(commit, build date, platform, NDJSON schema). `BuildCommit` and `BuildDate`
can be stamped the same way; see [compatibility](COMPATIBILITY.md).

## First run

Set a provider key in the environment of the terminal launching the client:

```sh
export GROQ_API_KEY='your-provider-key'
yonderllm providers
yonderllm ask "hello"
```

In PowerShell use `$env:GROQ_API_KEY='your-provider-key'`. Avoid committing keys
or putting them in shared shell history. Launch `yonderllm` for the TUI.

`yonderllm config init` creates an owner-readable starter configuration
(`--force` overwrites an existing one). `yonderllm config path` locates it. On
Unix keep it mode 0600 (`chmod 600 path`). Windows uses inherited ACLs; keep
the file in your private user profile.

## Shell completion

```sh
yonderllm completion bash   > /etc/bash_completion.d/yonderllm
yonderllm completion zsh    > "${fpath[1]}/_yonderllm"
yonderllm completion fish   > ~/.config/fish/completions/yonderllm.fish
yonderllm completion powershell | Out-String | Invoke-Expression
```

Completion covers subcommands, flags, provider names for `--provider`, and
model ids for `--model`.

See [providers](PROVIDERS.md) and [troubleshooting](TROUBLESHOOTING.md).
