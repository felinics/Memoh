# ACP Runtime State Contract

This directory owns the boundary between durable ACP agent configuration and
process-local agent state. ACP is the generic channel for custom,
user-supplied agents speaking the Agent Client Protocol; the only registered
profile is the generic `acp` profile (managed launch `command`/`arguments`,
`api_key` setup mode). Built-in External Agents (Codex, Claude Code, Grok
Build) are not ACP profiles — they run as direct external runtimes under
`internal/agent/runtime/external/codex`, `internal/agent/runtime/external/claudecode` and
`internal/agent/runtime/external/grok`.

Keep the boundary profile-driven. The launcher must not grow agent-name
conditionals.

## The ACP client is shared

`client/` is the protocol client: the connection, sessions, event mapping and
permission handling. It has two entry points.

- `Runner.StartSession` is the pool's: it launches one process for one
  session, advertises the client file and terminal capabilities, and looks the
  agent up in the profile registry.
- `Connect` returns a `Conn` over a process the caller started. A `Conn` hosts
  any number of sessions, delivers each inbound call to the session it names,
  and can resume a session the agent persisted. Updates are applied in the
  connection's order, so a request returns once the updates sent before its
  response have been applied. It advertises no client file or terminal
  capability and looks nothing up: everything agent-specific (`_meta`, MCP
  servers, how the agent names an MCP tool call) arrives in `SessionConfig`,
  and the agent's own methods go through `Call`, `Authenticate` and
  `Subscribe`, whose method names and shapes the caller owns.

On both, acp-go-sdk handles notifications on one goroutine and closes the
connection once 1024 are queued behind it. The client therefore stops reading
the agent's output while 512 updates wait, and the agent waits with it. Every
message behind a held update waits too, so a notification handler must never
wait, neither on the agent nor on a consumer: put an `external.TurnStream` in front of a
prompt's live stream.

A built-in agent whose own protocol is ACP (Grok Build) is a direct runtime
that uses `Connect`. It must not import the pool or `profile/`
(`internal/arch` enforces this), and the client must not learn its name: add
a field to `SessionConfig` or expose a response value instead.

## Required invariants

These hold for the processes and sessions the pool starts. A direct runtime
on `Connect` owns its own process environment and session continuity (Grok
Build resumes its sessions; see `docs/agent-runtime.md`).

- Every pool-started ACP process receives a fresh runtime directory under
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
- Conversation continuity is the agent's own session files. Thread runtime
  metadata keeps only the native session ID and the thread it belongs to
  (`acp_native_session`); never copy session contents into the database. A
  new process restores the session with `session/resume` when the agent
  offers it, and with `session/load` when it does not or refuses; history
  replayed while restoring reaches neither the live stream nor the
  transcript.
- A session is replaced with a new one only when the agent cannot restore
  it: it offers neither method, or it refused each one it offers with an
  error other than `auth_required`. A timeout, cancellation, broken connection or
  missing credential fails the turn and keeps the stored ID. A replacement
  tells the user through a `native_history_lost` notice on the next prompt.
- A restore that times out or breaks the connection is
  `client.ErrSessionRestoreFailed`. Only the user gives such a session up,
  through `DELETE .../acp-runtime/native-session`, which removes the stored ID
  under the history-reset gate and keeps the messages.
- A stored ID that belongs to another thread (fork, backup restore) and a
  discuss turn's fresh runtime always start a new session. Clearing history,
  or changing an empty thread's agent or project, closes the process and
  removes the stored ID. A prompt that never reached the agent records no ID.
- Process preparation runs under the bot's runtime-configuration guard
  (`RuntimeSyncGuard`): a stale configuration generation or an in-progress
  reset must not start a new runtime.

## Profiles

`profile.RuntimeStoragePolicy` declares launcher-owned environment bindings
(`HOME=/data`, runtime-local `TMPDIR`, shared `NPM_CONFIG_CACHE`).
`Profile.SetupModes` declares the setup modes allowed to start a process.

A profile is a kind of agent, not an agent: every custom agent on a bot shares
the one generic profile id. An Agent instance therefore owns its setup — the
managed fields live on its `bot_agents` row (`metadata.managed`), and warm
processes are matched and reset by instance id. Never key a setup or a process
by profile id alone. Resolve setups through
`botagents.Service.ResolveACPSetup`; instances that predate per-instance
setups still read the bot's `metadata.acp.agents.<profile>` slot, which is
read-only and must not gain new writers.

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
- a new process restores the stored session, and replayed history stays out
  of the live stream;
- each way an agent can fail a restore either replaces the session with a
  notice or fails the turn and keeps the stored ID;
- a copied stored ID and a discuss turn start a new session.

Run at minimum:

```sh
go test -race ./internal/agent/runtime/external/acp/...
go test ./...
golangci-lint run ./...
```
