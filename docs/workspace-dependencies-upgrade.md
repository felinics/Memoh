# Upgrading managed workspace dependencies

The workspace image supplies baseline runtimes and workspace tools. Codex and
Claude Code are managed dependencies rather than required image contents. Each
Bot has one confirmed dependency target; executable payloads may use workspace
local storage while metadata and Agent Homes remain persistent. There is no
dependency Rollback action or selectable previous-version history.

An existing supported CLI can still be discovered without installation. Chat,
device-code login, List and Preflight do not authorize a new script. A user with
Manage permission prepares and confirms an exact version and frozen recipe.
Successful installation authorizes restoration of that same target after payload
loss; adopting an existing executable does not.

## Storage and compatibility

Dependency payloads and caches use the fixed workspace path `/data/.memoh/deps`.
There is no dependency-store path setting. Agent Homes keep their existing paths.
Existing installations and unfinished operations retain their recorded paths.

Persistent `/data/.memoh/deps` retains state, launchers, receipts, composite-version
resolution metadata and `.execution-window.json` lifetime/admission evidence.
On Linux, kernel control uses the fixed local root `/run/memoh/deps`, independently
of the payload location. It holds operation locks under `.locks/`,
launch and installation leases under `.leases/`, and `.execution-window.lock`.
Non-default data roots use separate internal hashed namespaces. The image provides
a default directory, but adapters must preserve a writable path with working local
kernel locks after mounts and startup preparation. OSS binds host
`<workspace data root>/run/<bot>` at `/run/memoh`, hiding the image directory; the
host runtime directory must use a local filesystem, never NFS. The canonical root
runtime can create `deps` there. Cloud startup prepares the directory when `/run`
is tmpfs. Verify permissions for the actual runtime user; this upgrade does not
extend the UID/GID contract for arbitrary non-root custom images. Control files
may survive rootfs replacement, so lifecycle safety relies on whole-workspace
epoch evidence and kernel locks rather than their deletion.

macOS retains the existing data-root lock paths and `lockf` support. Codex retains
`/data/.codex/agents/<bot_agent_id>`; Claude Code retains `HOME=/data` and
`CLAUDE_CONFIG_DIR=/data/.claude`. Authentication files and their authority do not
move as part of this upgrade.

| Server/recipe combination | Behavior |
| --- | --- |
| New Server, isolated recipe | Unique installations in the configured store; Server-owned publication, recovery and cleanup |
| New Server, older frozen recipe | Original layout remains usable; no forced Home relocation or local-storage performance promise |
| Older runner, new compatible recipe | The recipe's old-runner branch retains its version-directory behavior |
| New Server, installed image/PATH CLI | Read-only discovery uses supported commands without creating recovery authority |

All 32 official recipes declare the isolated protocol in digest-covered script
comments. Existing frozen recipes are not silently upgraded. Live same-version
replacement with an unsafe legacy recipe requires confirmation of an isolated
revision. Downgrade is another confirmed installation of an older exact version,
subject to recipe pins and runtime support.

### Legacy Server/image boundary

Earlier Servers using the v3 contract required both
`/opt/memoh/workspace-contract.json` and bundled Codex/Claude Code launchers. They
cannot initialize a new baseline image by restoring only that JSON. When upgrading
from that generation, upgrade Server before replacing its matching image. New
Servers validate dependencies at use and can discover supported old-image CLIs
during the transition. This historical constraint is separate from the removed
dependency version rollback feature.

## Upgrade sequence

1. Use a maintenance window. Stop admitting new dependency operations and Agent
   work while changing Server or recreating workspaces. Drain old operations,
   including any old Rollback operation, with the old release's recovery procedure.
   Do not erase receipts or release uncertain claims based on age. Migration from
   Linux locks on `/data` requires a whole workspace restart and confirmation that
   old scripts have stopped before new operations use `/run/memoh/deps`. An idle
   new lock cannot prove that an old-path owner exited; restarting only the Server
   or bridge is insufficient.
2. Record running releases, effective configuration and actual image digests.
   Follow the deployment's backup procedure for a consistent database and complete
   persistent volume, including `.memoh` and Agent Homes. Mutable image tags and
   `if_not_present` are not compatibility guarantees.
3. Apply paired incremental migrations and deploy matching Server, Channel,
   bridge/workspace image, Web and API clients. Verify the actual mounted control
   directory is writable with working local locks, and complete the workspace
   restart before reopening admission.
   Legacy observations remain readable but receive no automatic
   recovery authorization. Old clients must not continue sending omitted versions
   or calling the removed Rollback endpoint.
4. Publish or acquire reviewed isolated definitions. In each Bot's Apps settings,
   prepare and confirm the exact target with a Manage-capable account. For legacy
   or adopted installations, recovery authorization performs the confirmed
   reinstallation before enabling automatic recovery.
5. Provision local storage where wanted. Changing the store root does not move an
   existing installation: its registered path remains in effect until a successful
   install/update/reinstall replaces it. Do not erase old directories to force
   discovery to choose the new root.
6. Check the executable, version, successful desired target and a fresh Agent
   session. On a disposable Bot, recreate the image/rootfs while retaining its
   volume; verify exact-version recovery, credentials, session state and enabled
   display features before enabling that flow for users.
7. Reopen traffic after runtime and UI verification. Pending cleanup follows the
   controlled restart rules below; it is not selectable version history.

One Bot's installation does not prepare another Bot. A baseline image does not
include managed Agent CLI installers or a populated recipe cache. An unprepared
Bot reports missing dependency/authorization instead of installing latest when
chat is retried.

For Cloud/E2B, verify the Bot's actual template selection and adapter configuration.
Building a template or changing a default does not update existing workspaces.
Local payload loss after destroy/recreate differs from pause/resume; verify both.
Production template changes and data cleanup require deployment authorization.

## Recovering payload loss

Only durable desired targets authorize recovery. At workspace readiness or explicit
execution preparation, recovery checks the registered managed copy even when a
toolkit/PATH fallback exists. It restores the same version using the stored
immutable recipe. A surviving verified payload can have its launchers restored
without another download.

Prerequisites needing installation require their own confirmed targets. Missing
authority, missing/corrupt definitions, retirement and unsupported platforms
require management attention. Transient failures use persistent retry state and
bounded backoff. Manage-only retry keeps the target; it is not an upgrade.

App removal revokes a dependency target when removal applies. Updates invalidate
stale queued repairs. List, refresh and Preflight only observe these states and
never enqueue installation.

## Interrupted transactions and pending cleanup

Receipts under `.memoh/deps/.operations` survive payload deletion. Recovery checks
the kernel lock, database-owned operation intent, script exit and actual payload
before finalizing database state. The database claim freezes the action, exact
version, publication and authorization identity; immutable receipt metadata must
match that intent exactly. Workspace receipt fields cannot grant or change that
authority. Missing or mismatched trusted intent is fenced under the kernel lock
and marked failed, retaining the previous usable payload and receipt evidence
without granting recovery authority. Recovery requires a new Manage confirmation.
Otherwise it uses the stored frozen version probe without needing the current
catalog's prerequisite graph, and validates the candidate path and exact version.
A lost stream is uncertain until those facts are
available. A completed receipt cannot make missing files healthy.

Linux runner, probes, finalization, cleanup and execution leases share the local
control namespace. The persistent execution-window record is deliberately kept
on `/data`, so a bridge restart cannot erase evidence of prior Exec/PTY admission.
Never delete cancellation markers or stable locks to reclaim ownership. A paused
old Server must remain fenced from resuming canceled work. Stopped or unreachable
workspaces retain intent until recovery can reach them. Pre-protocol records
without operation IDs and legacy directory locks need operator recovery after
the old Server and workspace processes have stopped.

Unpublished failed candidates can be cleaned after proven completion. Published
retired payloads remain pending until a whole Linux workspace restart provides a
bridge-confirmed startup window before ordinary Exec/PTY admission. Create/setup,
API Start and lazy startup run Manager's maintenance boundary before readiness.
Restarting only Server or bridge is
insufficient. Old bridges, non-Linux workspaces and missing lifetime evidence retain
pending payloads. Launch leases and lifetime checks also protect commands resolved
before their processes start. Held leases, bootstrap process references and
incomplete process-access evidence also defer cleanup. Backends without a supported
startup proof require operator-controlled maintenance for published payloads.

Cleanup validates every recorded path and excludes the current installation,
stable locks, Agent Homes and unrelated toolkit files. An explicitly recorded
legacy `versions/<version>` path can be retired; all legacy folders are not scanned
and deleted. There is no bulk legacy-cleanup CLI. Unknown residual directories and
prior custom store roots require operator review in a maintenance window. A
retained tree is not a supported rollback target.

Package caches have separate limits. Under the dependency lock, isolated operations
remove cache files older than seven days and discard caches above 512 MiB.
Persistent version-resolution metadata is excluded.

## Offline operation

Keep an already usable supported CLI in place while registry/download hosts are
unavailable. Before depending on ephemeral-store recovery, ensure exact definitions
and upstream artifacts remain available through an approved distribution path.
Persistent recipes alone do not provide offline binary installation.

```toml
[workspace_dependencies]
offline = true
catalog_refresh_interval_seconds = 600
update_check_interval_seconds = 86400
reap_interval_seconds = 60
discovery_cache_ttl_seconds = 600
```

Intervals shown are defaults; overrides use positive seconds. Offline mode disables
registry refresh and automatic upstream checks. It is not a network sandbox:
confirmed scripts and already authorized repairs may still download software.
Repair never substitutes a newer recipe for a missing frozen definition.

`[workspace_dependencies.script_env]` accepts only `NODEJS_MIRROR`,
`UV_RELEASES_URL`, `NPM_MIRROR` and `UV_PYTHON_INSTALL_MIRROR`; unrelated Server
variables are not inherited. Node.js and uv archive mirrors still require official
HTTPS release checksums. An archive mirror alone does not make installation offline.

## Deployment recovery

A failed Server deployment follows the deployment's release and consistent-backup
procedure. Restore a compatible Server/Channel/Web/image set when that procedure
calls for it. A database down migration neither restores deleted CLI payloads nor
undoes scripts. Restoring database and volume backups discards later changes and
must use a deliberate recovery point.

This guide promises neither arbitrary Server downgrades nor one-click CLI version
restoration. Current contracts and validation evidence are in the
[dependency design](design/workspace-dependencies.md) and
[validation record](design/agent-cli-storage-validation.md).
