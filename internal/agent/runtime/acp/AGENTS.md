# ACP Runtime State Contract

This directory owns the boundary between durable ACP agent configuration and
process-local agent state. Keep the boundary profile-driven. The launcher must
not grow agent-name conditionals when a new ACP agent is added.

## Required invariants

- Every ACP process receives a fresh, mode-specific runtime directory under
  `/tmp/memoh-acp-runtime/<agent>/<uuid>` with mode `0700`.
- Never put an agent's complete home on `/data`, and never symlink a runtime
  home back to `/data`.
- Stage only paths declared by the agent profile. Locks, SQLite/WAL files,
  sessions, history, sockets, PIDs, and caches stay process-local.
- `/data` may contain only profile-declared configuration and credentials.
  Managed Codex and Hermes files remain durable and are staged into each
  RuntimeLease; their locks, databases, sessions, and caches never are.
- Cleanup may remove only the UUID directory owned by that process. Startup
  failure, natural process exit, and explicit `Close` use the same finalizer.
- A failed durable sync preserves the UUID directory for manual recovery.
- Runtime files never delete durable files. Durable writes are atomic and must
  either use compare-and-swap or an agent-specific monotonic freshness rule.
- Below writable roots such as `/tmp`, inspect every existing path component
  before creating the next one and immediately recheck each created directory.
  Never call recursive mkdir on an unvalidated runtime or cache parent.
- Codex `auth.json` writes are ordered by its RFC3339 `last_refresh` value. A
  stale process must never overwrite a newer process or OAuth-handler token.
  Synchronize after each prompt and periodically while the process is alive.
- Sync cadence is deliberately asymmetric. Only `codex_auth` artifacts get the
  live per-prompt/periodic sync above; every `compare_and_swap` artifact
  (Claude Code and Hermes configuration and credentials, Codex self-mode
  trees) writes back only at process exit. A server- or container-level kill
  before exit loses agent-side changes from that session — CAS still
  guarantees the durable copy is never corrupted or replaced by stale data.
  This is the documented degraded tier: extending live sync to another agent
  requires a per-file freshness rule like Codex's, not a cadence change alone.

## Profiles

`profile.RuntimeStoragePolicy` is the source of truth. Each supported setup
mode must declare:

- agent process environment bindings;
- files or trees staged from `/data`;
- whether each staged artifact is read-only, compare-and-swap durable, or uses
  a specialized freshness strategy; and
- files generated directly in the RuntimeLease by Memoh.

Use the narrowest state variable supported by the pinned agent. Keep
`HOME=/data` when that variable covers all agent state; Hermes also receives a
runtime-local `HOME` because its pinned release still uses home-relative auth
paths.

| Agent | Process-local state variable | Managed durable data |
| --- | --- | --- |
| Codex | `CODEX_HOME` and `CODEX_SQLITE_HOME` | allowlisted `auth.json` and `config.toml` |
| Claude Code | `CLAUDE_CONFIG_DIR` | none; managed credentials use process environment |
| Hermes | `HERMES_HOME` | managed `config.yaml` and `.env` |

Hermes is the degraded tier. Its runtime-local `HOME` is ephemeral by design:
`$HOME`-relative files written by the agent or its subprocesses do not survive
the process; only explicitly allowlisted `HERMES_HOME` paths round-trip to
`/data`.

The ACP terminal/tool environment is separate from the agent environment. Its
working directory and `HOME` remain under `/data`; do not expose the agent's
temporary home to ordinary workspace commands. Preserve the existing
`CleanEnv` and `UnsetEnv` credential filtering.

Shared package caches may live under `/tmp/memoh-acp-cache`. They must remain
container-local and contain no credentials or mutable agent session state.

## Version and file audits

Built-in profiles launch the adapter binaries pinned by
`docker/toolkit/install.sh`; they must not resolve npm `latest` at runtime.
Before changing an allowlist, inspect the exact pinned adapter and agent source
to confirm:

1. the state/config environment variable it honors;
2. every credential or user-authored configuration path;
3. how it launches shell subprocesses; and
4. which files are locks, databases, sessions, histories, or caches.

Do not infer filenames from another release and do not allowlist an entire
agent home as a shortcut. Mixed config/state files, such as Claude Code's
`.claude.json`, require an explicit read-only or field-aware decision.

## Verification

Behavior-level coverage should include:

- all built-in profiles and setup modes pass policy validation with no path
  escape;
- consecutive processes get different runtime roots and never see each
  other's locks or databases;
- only allowlisted configuration survives close;
- stale Codex OAuth copies cannot replace newer durable credentials;
- startup failure removes its runtime root without changing `/data`;
- only profile-declared managed configuration and credentials appear on
  `/data`;
- pre-planted symlinks under `/tmp` (runtime and shared-cache tiers) fail the
  lease with no side effects at the symlink target;
- container replacement preserves durable config but not runtime state.

Run at minimum:

```sh
go test -race ./internal/agent/runtime/acp/...
go test ./...
golangci-lint run ./...
```
