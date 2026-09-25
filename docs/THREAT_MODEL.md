# yonderllm threat model

Intent: PRODUCTION

Baseline: commit `ce03af3` (the `v0.2.0-rc.2` tree) plus the uncommitted
hardening changes in the current working tree, listed under
[fixed since rc.2](#fixed-in-the-working-tree-since-v020-rc2). This document
describes the code at that point; it does not certify a published release.
Re-pin the baseline when the working-tree changes are tagged.

## System overview

Derived from [architecture](threat-model/architecture.md) and
[entities](threat-model/client.md). The system is an installable
outbound-network CLI/TUI with optional local tools, plaintext conversation
persistence, and a separate evaluation command.

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
   processes have the user's authority outside that root. Configured credential
   variables and a short well-known list are stripped from the child
   environment; every other inherited variable is exposed to approved programs.
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

### Fixed in the working tree since v0.2.0-rc.2

- **High — `--yes` waived confirmation of interpreters and launchers:**
  `Destructive()` was a name/verb table, so `sh -c`, `python -c`, `cmd /c`,
  `xargs rm`, `find -exec`, `git branch -D` and similar ran unprompted under
  auto-approve. Shells, script interpreters, launchers and force-style flags
  are now always confirmed; `.exe`/`.cmd`/`.bat`/`.ps1`/`.com` suffixes are
  stripped before matching. Evidence: `internal/shell` tests.
- **High — auto-approved writes could plant execution hooks:** `write_file`
  passed `destructive=false` for every path, so a model under `--yes` could
  write `.githooks/pre-commit`, `Makefile` or `package.json` and trigger it
  with a "harmless" command. Writes under dot-directories, to dotfiles and to
  build/manifest/tool-configuration files are now confirmed even under `--yes`;
  writes under `.git/` are refused in every mode.
  Evidence: `workspace.Hook`, `internal/tools` tests.
- **Medium — child processes inherited every API key:** `cmd.Env` was
  `os.Environ()`. Configured `api_key_env`/`header_env` names and a well-known
  credential list are now removed, case-insensitively. Evidence:
  `internal/shell` scrub tests.
- **Medium — credential-shaped files were plain `Allow` reads:** `.env`,
  `id_rsa`, `.netrc`, `.git/config` and similar are refused in code mode and
  confirmed in agent mode (including under `--yes`); reads under `.git/` and
  the other search-skipped directories are refused. Evidence:
  `workspace.Credential`, `internal/tools` tests.
- **Medium — `run --json` mislabelled the answering model after a fallback:**
  events now carry the `model` of the provider that produced them.
- **Low — `Workspace.Search` ignored cancellation:** the tree walk now
  returns `ctx.Err()`.
- **Low — TUI approval double-answer race:** a second keystroke could rewrite
  an allowed call as denied; answers are now recorded synchronously and a
  pending question is resolved as denied when the exchange ends.

### Fixed in v0.2.0-rc.2

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
- Writes recheck cancellation and a full bounded content snapshot after approval.
  A lossy diff summary is not used as identity. Unsnapshottable targets are
  refused. This is a best-effort stale-edit check, not atomic compare-and-swap.
- Undersized terminals disable affirmative approval while request details are
  hidden; denial remains available.
- Process output draining is bounded with `exec.Cmd.WaitDelay` (one second).
  Regression tests exercise a descendant holding pipes open and a running
  direct child stopped by a deadline. Descendant termination is not guaranteed.
- Full Linux `go test -race ./... -count=1` passed in WSL with Go 1.27 and GCC.
  Windows ordinary tests also pass. Live Surplus smoke results and pricing are
  recorded in [evaluation notes](EVALUATION.md).

## Residual risks and next verification

- Prompt injection can influence model requests. Per-action approval reduces
  impact but a user can approve a harmful command; automatic approval expands
  that exposure. Destructive-command and interpreter name heuristics are not a
  sandbox: a command the tables do not recognise runs unprompted under `--yes`.
- Approved child programs no longer see the configured credential variables,
  but inherit every other variable and can access network/other files.
  Portable process-tree termination and inherited-pipe handling require a
  dedicated OS-level regression suite.
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
