# Client, provider, tools, and storage entities

## Client interface and session

Availability: LOW_CRITICALITY (interactive utility; disruption interrupts work).
Assets: conversation integrity, unsaved answers, permission-state integrity.
Inputs: keyboard/CLI prompt, config, saved history, remote model/tool events.
Session state is single-owner mutable state; callers must serialize access.
TUI cancellation completion gating protects that ownership. Terminal content
may contain externally supplied control characters; display boundaries now
escape them visibly, while JSON and piped data preserve original values.

## Provider transport

Availability: LOW_CRITICALITY. Assets: API credentials, prompt confidentiality,
memory/CPU, provider billing. Untrusted HTTP bodies cross into parsers/history.
Redirects are refused. URL userinfo/query/fragment are rejected at config entry.
HTTP is allowed for user-configured endpoints; HTTPS defaults protect transport,
but do not protect against the provider itself. Configured fallbacks receive
conversation context. Error messages redact configured credentials and recognized
patterns. SSE aggregate 16 MiB, frame 1 MiB, errors 8 KiB, catalogue 16 MiB.
Catalogue reads use a limit+one sentinel and fail before JSON decoding.

## Tools, workspace, and process runner

Availability: LOW_CRITICALITY. Assets: workspace integrity, other user files,
inherited credentials, process privileges. Model call names/arguments are
untrusted. Chat has no tools. Code permits reads/searches and approved writes;
agent additionally permits approved exec. Automatic approval (`--yes`) is
explicit, agent-only, and does not waive confirmation of recognised destructive
or interpreter commands, hook-like writes, or credential-file reads;
classification remains heuristic. Tool definitions are not an authorization
boundary: tools and lower layers enforce policy.

`os.Root` confines built-in file access, including symlink escapes. It does not
isolate processes or exclude all sensitive files inside the workspace. Reads
cap actual bytes consumed and check regular-file type before/after open.
Replacement with a special file during open needs deeper cross-platform
verification. Writes recheck cancellation and the original content snapshot but are not
transactional compare-and-swap; a hostile concurrent writer can still race.

Commands use argv without implicit shell parsing, have 30-second contexts and
1 MiB captured-output caps, and inherit the environment minus configured
credential variables and a well-known list. Explicitly launching an interpreter
is still possible after confirmation. Parent cancellation is not a
portable descendant-process sandbox/kill guarantee. Descendant pipe inheritance
is bounded by a one-second drain grace period; process-tree termination and
filesystem-operation cancellation remain open follow-up checks.

## Persistence and distribution

Availability: LOW_CRITICALITY. Assets: plaintext history, usage-counter integrity,
installed executable/skill provenance. Saves validate names and tool-result
pairing, redact best effort, and write complete locked snapshots. Unix file
modes and Windows ACLs determine local-reader exposure. A local actor with the
same OS privileges can generally read/alter data and is not contained by the app.
Cross-platform CI includes Linux race testing; Windows local race execution
requires a C compiler/CGO. Installed development skills execute in the coding
agent's authority, not as yonderllm runtime plugins.

## Evaluation

Availability: LOW_CRITICALITY. Assets: reproducible comparisons and report privacy.
Fixture tools have no external side effects. Live mode is explicit and may incur
charges outside the application's persistent cap. Model-produced report content
is untrusted. The small suite cannot establish general model safety/quality.
