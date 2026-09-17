# Installation

Requires Go 1.27 or newer. Build the single executable from the repository:

```sh
git clone https://github.com/MoneyPack/yonderllm.git
cd yonderllm
go build -o bin/yonderllm ./cmd/yonderllm
./bin/yonderllm --version
```

On Windows use `go build -o bin/yonderllm.exe ./cmd/yonderllm`.
Put the executable on PATH. No model weights or inference runtime are installed.

Set a provider key in the environment of the terminal launching the client:

```sh
export GROQ_API_KEY='your-provider-key'
yonderllm providers
yonderllm ask "hello"
```

In PowerShell use `$env:GROQ_API_KEY='your-provider-key'`. Avoid committing keys
or putting them in shared shell history. Launch `yonderllm` for the TUI.

`yonderllm config init` creates an owner-readable starter configuration.
`yonderllm config path` locates it. On Unix keep it mode 0600 (`chmod 600 path`).
Windows uses inherited ACLs; keep the file in your private user profile.

See [providers](PROVIDERS.md) and [troubleshooting](TROUBLESHOOTING.md).
