# Scripting

`ask` prints answer text; `run --json` emits newline-delimited JSON. Notices go to
stderr in plain mode. Exit 0 means success, 1 means failure, 130 means interrupt.
Check the process exit code even when partial output exists.

```sh
printf 'Explain a mutex' | yonderllm ask
git diff | yonderllm ask --stdin "Write a commit message for this diff"
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

## Event schema

`run --json` emits newline-delimited JSON — one object per line, flushed as it
arrives, so a pipe stays live for the whole stream.

```json
{"schema_version":1,"type":"notice","notice":"groq unavailable, using gemini","provider":"gemini","model":"gemini-2.0-flash"}
{"schema_version":1,"type":"delta","delta":"one","provider":"gemini","model":"gemini-2.0-flash"}
{"schema_version":1,"type":"delta","delta":", two","provider":"gemini","model":"gemini-2.0-flash"}
{"schema_version":1,"type":"done","provider":"gemini","model":"gemini-2.0-flash","usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}
```

| Field | Type | Present on | Meaning |
| --- | --- | --- | --- |
| `schema_version` | int | all | `1`. Bumped only for a change that breaks an existing consumer; see [compatibility](COMPATIBILITY.md) |
| `type` | string | all | `delta`, `notice`, `tool`, `tool_result`, `done`, or `error` |
| `delta` | string | `delta` | The next fragment of the reply |
| `notice` | string | `notice` | Something worth knowing, such as a fallback or an output-token limit |
| `provider` | string | all | Provider that produced this event |
| `model` | string | all | Model that produced this event |
| `tool` | object | `tool`, `tool_result` | The call, and then how it ended |
| `error` | string | `error` | What went wrong |
| `usage` | object | `done` | `prompt_tokens`, `completion_tokens`, `total_tokens` |

A stream ends with exactly one `done` or one `error`. Notices are informational
and never terminal, which means a consumer can ignore every type it does not
recognise and still be correct. Before streaming begins, argument/config
failures are reported on stderr and the process exits nonzero without emitting
any event.

`provider` and `model` name the provider that *answered*, not the one that was
asked for: after a fallback the events switch to the fallback's provider and
its configured model, so a line is meaningful on its own. The only exception is
a failure before any provider was reached, which carries the session's active
provider and model.

Ignore unknown fields to remain compatible with additive releases. A closed
output pipe stops consumption and returns failure, instead of silently
reporting success.

### Tool events

A call and its outcome are two events, not one, because the call is worth
showing before the work has finished. They share an `id`, so a consumer can pair
them without depending on adjacency:

```json
{"type":"tool","tool":{"id":"call_1","name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}
{"type":"tool_result","tool":{"id":"call_1","name":"read_file","arguments":"{\"path\":\"go.mod\"}","result":"go.mod\nmodule github.com/MoneyPack/yonderllm\n"}}
```

(`schema_version`, `provider` and `model` elided for readability.)

| Field | Type | Meaning |
| --- | --- | --- |
| `id` | string | Ties a `tool_result` to its `tool` |
| `name` | string | `read_file`, `search_files`, `write_file`, or `run_command` |
| `arguments` | string | The JSON the model sent, verbatim |
| `result` | string | What the tool returned, on success |
| `error` | string | Why the tool refused, instead of `result` |

`arguments` is a string and not an object on purpose: it is the model's own
JSON, passed through unaltered. yonderllm will not reformat a malformed call
into something that looks valid, so what you see is what the model actually
asked for.

A `tool_result` carries exactly one of `result` or `error`. A refused call is
not a failed turn — the message goes back to the model, which usually corrects
itself and answers, and the stream still ends in `done`. A denied approval is
reported as a `result` (the refusal text the model received), not an `error`.

### Recipes

Watch what the model reads:

```sh
yonderllm run --mode code --json "$PROMPT" \
  | jq -r 'select(.type=="tool") | "\(.tool.name) \(.tool.arguments)"'
```

Reassemble a reply:

```sh
yonderllm run --json "$PROMPT" | jq -rj 'select(.type=="delta").delta'
```

Fail a script on a provider error:

```sh
yonderllm run --json "$PROMPT" \
  | jq -e 'select(.type=="error") | halt_error(1)' >/dev/null
```

## Limits and modes

`--stdin` explicitly appends piped input to a prompt argument with a blank line
between them. Without it, arguments take precedence over stdin, preserving the
existing behavior. Prompts are limited to 4 MiB; streams to 16 MiB per provider
round (and 1 MiB per SSE line). The configured timeout can also bound duration.

Noninteractive runs have no consent UI, so a tool the mode would *ask* about is
withheld from the model: `--mode code` exposes `read_file` and `search_files`
only, and `--mode agent` without `--yes` exposes the same two. `--mode agent
--yes` adds `write_file` and `run_command`, but recognised destructive or
interpreter commands, writes to hook-like files, and reads of credential-like
files still require interactive confirmation and are refused when nobody can
give it. See [safety](SAFETY.md#what---yes-does-and-does-not-waive).

`run` with no prompt argument, no `--json`, no `--stdin` and a terminal on
stdin opens the interactive session instead of reading a prompt; give it a
prompt or pipe one in when scripting.

This subprocess/NDJSON interface is the external integration contract. Custom
providers plug in via configuration; the executable does not load arbitrary
plugins or run an HTTP daemon. See [safety](SAFETY.md).
