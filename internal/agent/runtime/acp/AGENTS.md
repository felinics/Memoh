# ACP Runtime State Contract

This directory owns the boundary between durable ACP agent configuration and
process-local agent state. ACP is the generic channel for custom,
user-supplied agents speaking the Agent Client Protocol; the only registered
profile is the generic `acp` profile (managed launch `command`/`arguments`,
`api_key` setup mode). Built-in External Agents (Codex, Claude Code) are not
ACP profiles — they run as direct external runtimes under
`internal/agent/runtime/codex` and `internal/agent/runtime/claudecode`.

Keep the boundary profile-driven. The launcher must not grow agent-name
conditionals.

## Required invariants

- Every ACP process receives a fresh runtime directory under
  `/tmp/memoh-acp-runtime/<agent>/<uuid>` with mode `0700`. It holds only
  ephemeral state (`TMPDIR`) and is removed when the process exits.
- The agent's durable state lives directly under `HOME=/data` (the workspace
  volume). Memoh stages nothing into the runtime directory and syncs nothing
  back out of it; the agent owns its own files on `/data`.
- Never symlink a runtime path back to `/data`.
- Cleanup may remove only the UUID directory owned by that process. Startup
  failure, natural process exit, and explicit `Close` use the same finalizer.
- Below writable roots such as `/tmp`, inspect every existing path component
  before creating the next one and immediately recheck each created directory.
  Never call recursive mkdir on an unvalidated runtime or cache parent.
  Pre-planted symlinks under `/tmp` (runtime and shared-cache tiers) must fail
  the lease with no side effects at the symlink target.
- Conversation continuity is Memoh's context document. Every ACP session
  starts fresh; each completed turn publishes an explicit reset head
  (`agent_session_publications`) in the same transaction as the round's
  messages. The durable head is the single authority: a warm process records
  the head its native conversation corresponds to and compares it against the
  durable head before every prompt, so a session advanced elsewhere is
  restarted instead of silently diverging.
- Process preparation runs under the bot's runtime-configuration guard
  (`RuntimeSyncGuard`): a stale configuration generation or an in-progress
  reset must not start a new runtime.

## Profiles

`profile.RuntimeStoragePolicy` is the source of truth. Each profile declares
its launcher-owned environment bindings (`HOME=/data`, runtime-local `TMPDIR`,
shared `NPM_CONFIG_CACHE`) and the setup modes allowed to start a process.

The ACP terminal/tool environment is separate from the agent environment. Its
working directory and `HOME` remain under `/data`; do not expose the agent's
runtime directory to ordinary workspace commands.

Shared package caches may live under `/tmp/memoh-acp-cache`. They must remain
container-local and contain no credentials or mutable agent session state.

## Verification

Behavior-level coverage should include:

- the generic profile and its setup modes pass policy validation with no path
  escape;
- consecutive processes get different runtime roots;
- startup failure removes its runtime root without changing `/data`;
- pre-planted symlinks under `/tmp` fail the lease with no side effects;
- a completed prompt records a reset publication head under the round's fence;
- a legacy checkpoint head found on cold start logs a warning and starts a
  fresh session.

Run at minimum:

```sh
go test -race ./internal/agent/runtime/acp/...
go test ./...
golangci-lint run ./...
```
