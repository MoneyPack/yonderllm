KB_SNAPSHOT: UNPINNED
# yonderllm threat model

Intent: PRODUCTION

## System overview

Derived from [architecture](architecture.md) and [entities](entities/client.md).
The system is an installable outbound-network CLI/TUI with optional local tools,
plaintext conversation persistence, and a separate evaluation command. This is
an unpinned working-tree model; it does not certify a published release.

## Trust boundaries

1. **User/config to session:** endpoint choice, model, fallback list and permission
   mode are privileged local configuration. A repository file or provider reply
   is not authorized to change them.
2. **Client to provider:** prompts and credentials leave the machine. A fallback
   is another recipient of context; visibility does not replace consent to the
   configured chain. TLS protects transport, not provider retention/use.
3. **Provider to parser/session:** remote JSON/SSE, names, text, calls and usage
   are untrusted. Bound allocations and reject invalid protocol state.
4. **Model to local tool:** a requested capability is not permission. Policy,
   argument validation and approval precede execution. Tool output may contain
   prompt injection and must remain data, not system authority.
5. **Tool to OS:** rooted file operations constrain built-in paths; approved
   processes have the user's authority outside that root. Environment inheritance
   exposes credentials to approved programs.
6. **History to disk/display:** plaintext saves and terminal-rendered content
   have different exposure risks. Redaction is not encryption or a complete
   secret detector. UI labels cannot make untrusted content trustworthy.
7. **Development inputs to build agent:** skills and dependencies are executable
   guidance/code in a separate authority domain from the shipped CLI.

## Threat actors and vectors

- Malicious/compromised provider: oversized responses, invented tool calls,
  misleading approvals, malformed streams, secret echo, misleading usage.
- Repository/content author: prompt injection through files/search output,
  hostile filenames/control sequences, scripts executed after approval.
- Network adversary: interception against explicitly configured plaintext HTTP;
  trusted proxy and certificate configuration affect HTTPS guarantees.
- Local actor with filesystem access: history disclosure/tampering, workspace
  changes between approval and use, usage-counter manipulation.
- Prospective extension publisher: malicious update, capability overreach,
  credential exfiltration. This actor has no current runtime plugin entrypoint.

## High-risk assets

- Provider credentials and other secrets inherited by processes.
- Workspace and other files writable by the OS user.
- Conversation confidentiality and integrity, including fallback recipients.
- Correct permission decisions and correspondence between displayed and executed
  actions.
- Request spending and resource availability. Availability tier for each current
  entity is LOW_CRITICALITY; financial/data loss can still be serious.

## Current controls and findings

### Fixed in this working tree

- **Medium — unbounded model catalogue:** a provider-controlled large JSON body
  could exhaust client memory. Catalogue reads now stop at 16 MiB + one sentinel
  byte and reject oversized data before decoding.
  Evidence: `TestModelsRejectsOversizedResponse`.
- **Medium — post-cancel session overlap:** cancellation previously released
  input before worker completion, allowing a new prompt or slash command to
  race mutable session history/tools. Input is now preserved and deferred until
  the completion channel closes.
  Evidence: `TestCancelledWorkerBlocksNewInputWithoutDiscardingIt` plus existing
  retry/mode lifecycle tests.

### Existing mitigations

Redirect refusal, credential redaction in errors, strict endpoint shape,
bounded SSE/error parsing, bounded retry/tool rounds, no retry/fallback mixing
after partial text, tool-disabled explicit retry, rooted path handling,
deny-by-default policy, and locked snapshot/usage persistence address concrete
disclosure, tampering, privilege escalation, and resource-exhaustion threats.
Existing tests in provider/security, provider/stream_error, session/retry,
workspace/workspace, tools/tools, perm/perm, and session/sessions cover these
boundaries. This is evidence of specific controls, not proof of complete safety.

## Follow-up hardening and verification

- Display boundaries now visibly escape C0/C1, ESC/OSC introducers, and bidi
  control characters before applying TUI styles. Approval targets escape line
  breaks too. CLI terminal stdout and stderr use a UTF-8-aware escaping writer;
  JSON and piped stdout preserve data. Tests cover byte-split sequences and
  approval content. Natural-language spoofing remains possible.
- Regular-file reads/searches now limit actual bytes, not merely stat size.
  Direct reads reject known special files and recheck the opened handle type.
- Writes recheck cancellation and displayed diff after approval. This is a
  best-effort stale-edit check, not atomic compare-and-swap.
- Process output draining is bounded with `exec.Cmd.WaitDelay` (one second).
  Regression tests exercise a descendant holding pipes open and a running
  direct child stopped by a deadline. Descendant termination is not guaranteed.
- Full Linux `go test -race ./... -count=1` passed in WSL with Go 1.27 and GCC.
  Windows ordinary tests also pass. Live Surplus smoke results and pricing are
  recorded in [evaluation notes](../../../EVALUATION.md).

## Residual risks and next verification

- Prompt injection can influence model requests. Per-action approval reduces
  impact but a user can approve a harmful command; automatic approval expands
  that exposure. Destructive-command name heuristics are not a sandbox.
- Approved child programs inherit credentials and can access network/other
  files. Portable process-tree termination and inherited-pipe handling require
  a dedicated OS-level regression suite.
- Files can change after the approval recheck. Replacement with a special file
  between precheck and open, and cancellation during traversal still need
  cross-platform adversarial fixtures.
- Terminal control escaping covers the identified display sinks. Piped raw
  output is explicitly data, and downstream display consumers must escape it.
- Plaintext saved history and heuristic redaction leave disclosure risk. Same-user
  hostile processes are outside the app's containment capability.
- Catalogue/stream limits bound bytes, not every parsing CPU case. Fuzzing and
  a dependency-vulnerability scan were not completed in this pass.
- General concurrency safety of public Session methods is not promised. The
  current UI serializes input against the worker; future callers must do so too.

## Prospective provider/plugin ecosystem — design constraints only

No loader or extension protocol is implemented by this work. Before one is:

- Keep third-party executable adapters out of the main process; use explicit,
  versioned message contracts with byte/time limits and cancellation semantics.
- Grant capabilities per extension and per workspace. No ambient API-key or
  whole-environment forwarding; bind credentials to the intended endpoint.
- Define installation provenance, pinned versions/hashes, update approval,
  revocation and downgrade policy. Treat descriptions/manifests as untrusted.
- Require host-enforced policy for every tool request, even if an extension
  claims an earlier approval. Bind approval to the exact action and target.
- Separate extension failures from conversation state; require termination,
  resource budgets, replay/idempotency rules, and complete tool-result pairing.
- Test spoofed capability claims, cross-workspace access, credential routing,
  malformed/oversized IPC, cancellation, and failed upgrades before enabling it.

## Evaluation and review gates

Offline harness fixtures prove scoring/session integration only. Live comparisons
must retain provider/model/date/settings and separate answer, tool and latency
results. Human review remains necessary for open-ended correctness and approval
clarity. Before release, run full tests/build/vet, Linux race and OS-specific
filesystem/process checks; review residual findings rather than describing this
document as a clean security audit.
