# ACP Runtime State Contract

This directory owns the boundary between durable ACP agent configuration and
process-local agent state. ACP is the generic channel for custom,
user-supplied agents speaking the Agent Client Protocol, all run through the
one fixed `acp` profile (managed launch `command`/`arguments`, `api_key`
setup mode). Built-in External Agents (Codex, Claude Code, Grok
Build) are not ACP profiles — they run as direct external runtimes under
`internal/agent/runtime/external/codex`, `internal/agent/runtime/external/claudecode` and
`internal/agent/runtime/external/grok`. Grok Build speaks ACP, so it runs on
this package's core as an extension of its own (below).

Keep the boundary profile-driven. The launcher must not grow agent-name
conditionals.

## One ACP core, two extensions

`Host` is the ACP core: it leases agent processes from the supervisor
(`agentproc`), keeps each conversation's session on them (`Binding`), hands a
conversation over between processes, runs turns (configuration, Memoh tool
events, cancellation, failure), and mounts Memoh's tools into each
conversation's session through `toolmount`, as Codex does. An `Extension`
says what differs between kinds of ACP agent: how the process starts and
what it starts from (`Launch`), which session a conversation continues and
what else it opens under (`Session`), what a newly opened session needs
(`Opened`), and how a turn's selections apply (`Configure`). There are two:

- `SessionPool` (this package): user-supplied agents, configured by a
  profile and the bot's setup of it.
- `grok.Driver`: Grok Build, configured by its Agent; `_x.ai` extension
  methods, `GROK_HOME` and its sign-in are its own.

`client/` is the protocol client both use: the connection, sessions, event
mapping and permission handling.

- `StartProcess` launches a user-supplied agent with `HOME=/data` and the
  workspace shell's PATH (see the invariants below). Grok's `Launch` starts
  its CLI on a clean environment instead; both go through `agentproc`.
- `Connect` / `ConnectWith` perform the handshake. A `Conn` hosts sessions,
  delivers each inbound call to the session it names, and applies updates in
  the connection's order, so a request returns once the updates sent before
  its response have been applied. `ConnectWith` can advertise the client file
  and terminal capabilities (served from the bot's workspace through
  `SessionConfig.Workspace`) and elicitation.
- `Conn.OpenSession` restores a stored session (`session/resume`, then
  `session/load`) and creates a new one only when the agent cannot restore it.
- Everything agent-specific arrives through the extension points:
  `SessionConfig` (`_meta`, MCP servers, how the agent names an MCP tool
  call), `Call`, `Authenticate`, `Subscribe` and the prompt
  result's `Meta`, whose method names and shapes the caller owns.

ACP lets one connection host several concurrent sessions ("Each connection
can support several concurrent sessions", ACP architecture overview), and no
capability declares otherwise, so an Agent keeps **one process for all of its
conversations**, each on a session of its own (a `Binding`). This holds for
Grok Build too. An agent that cannot host concurrent sessions does not
follow ACP and is not supported. The same process answers what a new
conversation could choose (`GET /bots/{bot_id}/acp-options`) from a session
of the Agent's own in the draft's project, since an agent offers commands and
settings by its working directory. There are no pre-session runtimes: choices
made before a conversation exists travel in its creation request as runtime
metadata.

A process's fingerprint covers what the process starts from — the instance's
resolved launch command and setup, workspace backend and the bot's runtime
configuration generation — so a change replaces the process on the next lease.
What belongs to a conversation lives on its session, not the process: its
project (the session's cwd), owner, configuration generation, Memoh tool
route and identity, and the lock that keeps its turns and configuration
changes one at a time. A change to any of them reopens that session only.

Processes and conversations' sessions follow the lifecycle model in
`../agentproc/AGENTS.md` (states, the "out of service" and "gone" signals, and
who waits on which).

Rules for a shared process:

- A conversation's session is reused while it fits: same project and
  extension variant (a custom agent's owner and generation, Grok's
  permission mode), and the stored native session ID still names it, or
  still says what it said when the session opened (the new ID is recorded
  after the turn that used it). An ID cleared after a prompt is a history
  reset, as for a Codex thread: the next turn opens a new session.
- A session at work is never replaced, though it no longer fits: the
  request fails as runtime busy instead of cancelling the turn.
- A session that opened without Memoh's tools says so on every turn, and is
  opened again once they can be mounted.
- A session is never continued on two processes at once. Measured on Grok
  Build: an old process left holding a session a new one resumed keeps
  answering from its stale context while the files on disk hold both turns.
  So before a conversation opens on a replacement process, its session on the
  old one is closed (`session/close` when the agent offers it); without
  `close`, no turn of the conversation reaches the old process again.
- A failed turn or configuration change drops that session only; the next
  turn reopens it. A cancellation the agent never confirmed may hold a write
  on the connection every session shares, so it also retires the process:
  its other turns finish on it and the conversations move on.
- A discuss turn runs on a throwaway session on the same process, closed when
  the turn ends.
- A closing session cancels only the approvals and questions its own running
  prompt waits on.

On both, acp-go-sdk handles notifications on one goroutine and closes the
connection once 1024 are queued behind it. The client therefore stops reading
the agent's output while 512 updates wait, and the agent waits with it. Every
message behind a held update waits too, so a notification handler must never
wait, neither on the agent nor on a consumer: put an `external.TurnStream` in front of a
prompt's live stream.

Grok Build must not import `profile/` (`internal/arch` enforces this), and
neither the core nor the client may learn its name: add a field to
`SessionConfig` or the `Extension` contract, or expose a response value,
instead.

## Required invariants

These hold for the processes and sessions `SessionPool` starts. Grok Build
owns its own process environment and keeps its session id under its own key
(see `docs/agent-runtime.md`).

- A pool-started ACP process runs with `HOME=/data` (the workspace volume),
  `NPM_CONFIG_CACHE=/tmp/memoh-acp-cache/npm`, and the PATH the workspace
  shell reports, so an agent installed from the workspace terminal is found.
  Memoh prepares no directory for it and stages or syncs nothing: the agent
  owns its own files on `/data`.
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
  under the history reset and keeps the messages.
- A stored ID that belongs to another thread (fork, backup restore) and a
  discuss turn's fresh runtime always start a new session. Clearing history,
  or changing an empty thread's agent or project, removes the stored ID under
  the history reset. A prompt that never reached the agent records no ID.
- There is one history reset for every runtime (`sessionruntime`): it stops
  and waits out the runs in scope, then the supervisor closes that scope's
  processes (`DrainConversations`) and holds new ones off until the reset is
  released. A conversation's reset closes the process of a conversation's
  own; a shared process stays, and the conversation's next turn opens a new
  session because the stored ID is gone. A bot's reset closes every process
  of the bot, whatever runtime runs it. This package keeps no reset path of
  its own.
- A process starts only on the bot's current runtime-configuration
  generation. The start checks it before running the agent — a start that
  waited out a reset resolved its command on the old configuration — and
  again once the agent is up, closing one whose configuration changed
  meanwhile before it serves anyone. An in-progress reset holds new
  processes off.
- Model and reasoning selection come only from ACP session config options
  (`category: model` / `thought_level`, set with `session/set_config_option`).
  The former `models` field and `session/set_model` are not read: an agent
  that offers only those runs on its own default model.
- A custom agent is offered Memoh's tools over HTTP MCP whether or not it
  advertises `mcpCapabilities.http`; few adapters do, though most take it.

## Profiles

`profile` holds one fixed profile, `acp`: the managed `command` and
`arguments` fields an instance launches with, the setup modes and workspace
backends allowed to start a process, and which fields are secrets. There is no
registry; tool-title heuristics live in `client/titles.go` and apply to every
agent.

A profile is a kind of agent, not an agent: every custom agent on a bot shares
the one generic profile id. An Agent instance therefore owns its setup — the
managed fields live on its `bot_agents` row (`metadata.managed`), and
processes are keyed by instance id. Never key a setup or a process
by profile id alone. Resolve setups through
`botagents.Service.ResolveACPSetup`; instances that predate per-instance
setups still read the bot's `metadata.acp.agents.<profile>` slot, which is
read-only and must not gain new writers.

The ACP terminal/tool environment is the agent's environment: its working
directory and `HOME` remain under `/data`.

Shared package caches may live under `/tmp/memoh-acp-cache`. They must remain
container-local and contain no credentials or mutable agent session state.

## Verification

Behavior-level coverage should include:

- the process environment owns `HOME`, `PATH` and `NPM_CONFIG_CACHE`, and a
  failed PATH probe falls back to the default PATH;
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
