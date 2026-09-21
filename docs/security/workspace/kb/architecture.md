# yonderllm architecture knowledge base

Source baseline: `c32c1c2` plus the uncommitted safety/TUI/evaluation changes in
this workspace. This is an unpinned working-tree review, not a release audit.

## Operated system

yonderllm is a user-invoked, installable Go executable for remote model inference.
`cmd/yonderllm/main.go` delegates to `internal/cli`. Interactive use runs Bubble
Tea; scripting uses text or schema-1 NDJSON. There is no inbound listener or
runtime plugin loader in the current product.

Configuration flows from defaults, owner-protected TOML (Unix), environment,
and command flags into a lazy provider resolver. Configured HTTP(S) provider
endpoints receive prompts and Bearer credentials through `ChatCompat`. Both
chat streaming and model enumeration are outbound HTTP operations.

`internal/session` owns conversation history, fallback/retry policy, bounded tool
rounds, and usage. Tools are selected by `internal/perm` and constructed by
`internal/tools`; approvals occur there before workspace writes or process
execution. Workspace file access uses `os.Root`. Approved programs execute with
the user's OS permissions, inherited environment, and network access.

Stream events reach the TUI through a worker goroutine/channel. Cancellation
signals that worker; a completion channel now prevents new input from accessing
Session until the cancelled worker exits. The interface displays provider/model
and mode, with mode prioritized on narrow screens.

Saved conversations are local, unencrypted JSON. A file lock and temporary-file
rename protect complete snapshots; redaction is heuristic. A separate persistent
usage counter uses process and file synchronization. It counts logical requests,
not provider billing.

`cmd/yonder-eval` is a separate development command. Offline providers are
deterministic fixtures. Explicit live runs send synthetic tasks to one selected
provider, with in-memory tools and no fallback/retries/session persistence.

## Evidence

- `internal/config/config.go`: load/validate endpoints, environment credentials.
- `internal/cli/resolver.go`: lazy adapter construction, policy composition.
- `internal/provider/chatcompat.go`: redirect refusal, SSE and catalogue limits.
- `internal/session/session.go`: history, retries, fallback, tool rounds.
- `internal/tools/tools.go`, `internal/perm/perm.go`: capability and approval policy.
- `internal/workspace/workspace.go`: rooted file access and bounded result policy.
- `internal/shell/shell.go`: argv execution, inherited environment, output cap.
- `internal/session/sessions.go`, `usagestore.go`: local persistence.
- `internal/tui/stream.go`, `update.go`: event delivery and stop lifecycle.

## Future design scope

Third-party executable providers/plugins are prospective only. A future design
would introduce code distribution, version negotiation, capability manifests,
credential scoping and process isolation. None of those controls exists merely
because this document recommends them. The user requested coverage of that
future ecosystem without implementing a loader.
