# Scripting

`ask` prints answer text; `run --json` emits newline-delimited JSON. Notices go to
stderr in plain mode. Exit 0 means success, 1 means failure, 130 means interrupt.
Check the process exit code even when partial output exists.

```sh
printf 'Explain a mutex' | yonderllm ask
yonderllm run --json "Explain a mutex" | jq -r 'select(.type=="delta").delta'
```

```powershell
yonderllm run --json "Explain a mutex" |
  ForEach-Object { $e = $_ | ConvertFrom-Json; if ($e.type -eq 'delta') { $e.delta } }
```

Python can launch the same executable without a server:

```python
import json
import subprocess

with subprocess.Popen(
    ["yonderllm", "run", "--json", "Explain a mutex"],
    stdout=subprocess.PIPE, text=True, encoding="utf-8",
) as process:
    for line in process.stdout:
        event = json.loads(line)
        if event["type"] == "delta":
            print(event["delta"], end="", flush=True)
    if process.wait() != 0:
        raise SystemExit("yonderllm failed; partial output is not a complete answer")
```

Events are `delta`, `notice`, `tool`, `tool_result`, `done`, or `error`. Tool IDs
match start/result events; a `done` event means the exchange completed. Before
streaming begins, argument/config failures are reported on stderr.

Noninteractive runs have no consent UI. Read-only code mode can expose reads and
searches. Writes require explicit agent mode and `--yes`; destructive commands
still require interactive confirmation and are refused when nobody can approve.

This subprocess/NDJSON interface is the external integration contract. Custom
providers plug in via configuration; the executable does not load arbitrary
plugins or run an HTTP daemon. See [safety](SAFETY.md).
