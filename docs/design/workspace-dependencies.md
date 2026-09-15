# Workspace dependencies from Supermarket

## Ownership and definitions

Supermarket publishes the official `memoh` dependency registry. Memoh owns verified
artifact loading, discovery, script execution, installation transactions,
authorized recovery and runtime integration. It does not embed production
recipes. A recipe correction is a registry release; adding an Agent runtime driver
still requires Memoh code. The initial five recipes have expanded to 32 official
recipes supporting the isolated payload protocol.

Apps compose Skills, dependencies and connectors. Dependencies keep independent
releases; an App references them and prepares the exact installations that need
confirmation. See [Apps](apps.md) and the [upgrade guide](../workspace-dependencies-upgrade.md).

A definition artifact contains `dependency.yaml`, referenced POSIX sh scripts and
an optional icon. Manifest version 1 describes identity, translations, category,
source, prerequisites, commands, platforms, optional version pin and timeouts.
Unknown fields and unsupported schema versions are rejected.

| Method | Supermarket path | Meaning |
| --- | --- | --- |
| GET | `/api/dependencies` | Current catalog with filtering and pagination |
| GET | `/api/registries/memoh/dependencies/{id}` | Current descriptor |
| GET | `/api/registries/memoh/dependencies/{id}/releases/{revision}` | Immutable release |
| GET | `/api/artifacts/dependency/{digest}` | Immutable compressed artifact |

Dependency snapshots and `dependencies.lock.json` are independent of Skill
snapshots. Immutable blob writes and conditional updates protect their pointers.
Three identities serve different purposes:

- **Definition revision:** SHA-256 of the immutable release JSON bytes.
- **Artifact digest:** SHA-256 of the compressed archive, with declared compressed,
  uncompressed, serialized-tar and file-count budgets.
- **Software version:** the canonical identifier confirmed for installation and
  verified against the recipe result before publication.

The manifest digest frames sorted manifest/script filenames, lengths and contents
as `name + NUL + byte length + NUL + contents`. Icons have separate digests. Memoh
validates release, archive, manifest projection, scripts and icons before execution.
Links, unsafe or duplicate paths, undeclared files and exceeded budgets are rejected.
The publisher validates prerequisite references and rejects cycles.

## Preparation and confirmation

Management preparation freezes a definition revision and exact software version
before confirmation. For a direct dependency operation, the client supplies the
revision from the catalog or script preview. A blank version during preparation
resolves the recipe pin or runs that frozen recipe's `check_update` action.
Preparation may start a workspace for that check; it never provisions a dependency
or grants automatic recovery authority.

Install, update and reinstall requests require both `version` and
`definition_revision`. Missing values, mutable aliases and ranges cannot authorize
an installation. The recipe must report the confirmed canonical version unchanged;
a partial selector cannot silently expand into another target. Generated recipes
also use distribution labels, four-part versions and composite identifiers, so the
protocol is not limited to three-part SemVer.

App preparation freezes the App release and every dependency operation that will
run a script. Referencing an existing usable dependency does not authorize its
future reinstallation. A publication between confirmation and execution cannot
replace the frozen recipe.

Install, update, reinstall, explicit downgrade and repair share provision and
commit. Reinstall allocates a new physical installation even for the same version.
Downgrade means confirming an older version for a new installation. There is no
dependency Rollback action, historical-version selector or `previous_version`
response field.

## Persistent state and availability

`bot_dependency_installations` records observations and current operation state.
`bot_dependency_desired_installations` holds one authorized target per
Team/Bot/dependency: exact version, publication identity, desired revision,
authorization identity, registered payload and repair scheduling state. Successful
confirmed installation commits this target. Failed installation, discovery,
adoption of an existing command and App reference creation do not grant it.

Authorization events are durable audit records, not binary history. Removing a
managed dependency revokes its target; queued repair cannot recreate it. An
accepted update invalidates earlier queued work, and successful commit replaces
the target. Queueing, claiming and finishing repairs compare desired revision
and operation identity in addition to filesystem locks.

Memoh accepts `memoh` releases from the configured `supermarket.base_url` origin.
Artifact URLs derive from verified digests; redirects remain on that origin.
PostgreSQL caches immutable release/archive bytes under source URL, registry,
dependency ID and revision. Connection-bound team RLS isolates records, and catalog
pointer updates use generation CAS.

Definition collection retains current catalog references, current installation
definitions, active operation definitions and authorized targets. Other definitions
have a 30-day access grace period, renewed on lookup. Historical authorization
events do not indefinitely pin old recipes or payloads.

- Server startup does not require Supermarket. A background worker refreshes the
  catalog; offline mode disables registry refresh and automatic upstream checks.
- Availability failures preserve the last verified snapshot and mark it stale.
  Invalid content cannot replace the cache.
- A retired definition cannot authorize new installation or automatic repair.
  Retained immutable bytes are not permission to bypass retirement.
- Discovery finds supported managed, image and PATH commands with a cold catalog
  cache. Read-only discovery never enqueues installation.
- Repair reads the stored, verified publication for its authorized target. It
  cannot replace an unavailable definition with the current registry release.
- Recipe caching is not binary caching. Installs and previously authorized repairs
  may still need upstream downloads; offline mode is not a network sandbox.

## Workspace layout

Management uses only the Bot's native workspace. A selected user computer or
remote target is not an installation target. Agent Home and credential ownership
remain unchanged: Codex uses `/data/.codex/agents/<bot_agent_id>`, including
`auth.json`; Claude Code keeps `HOME=/data` and `CLAUDE_CONFIG_DIR=/data/.claude`.

Dependency payloads and caches use the fixed workspace path `/data/.memoh/deps`.
There is no dependency-store path setting. The workspace image prepares the
required directories; metadata and Agent Homes remain persistent.

```text
/data/.memoh/deps/
  bin/                                  # Stable generated launchers
  .execution-window.json                # Persistent lifetime/owner/admission proof
  .operations/<dependency>/
    <operation_id>/                     # Durable transaction receipt
    .cleanup/<operation_id>.json         # Pending payload cleanup
  <dependency>/
    state.json                          # Committed observation, not authority
    current -> <store>/<dependency>/installs/<installation_id>
    resolutions/                        # Composite version metadata, when needed

<store>/<dependency>/
  installs/<installation_id>/            # Unique candidate or current payload
  .staging-<operation_id>/               # Per-operation staging, when used
  cache/                                # Disposable package cache

/run/memoh/deps/                         # Linux local kernel control, default /data
  .locks/<dependency>.lock              # Operation and finalization lock
  .leases/<dependency>.lock             # Launch admission lease
  .leases/<dependency>-<installation_id>.lock  # Running installation lease
  .execution-window.lock               # Exec/PTY and maintenance admission lock
```

Linux kernel control uses the fixed local root `/run/memoh/deps`, independently of
the payload location; a non-default data root gets a separate internal hashed
namespace. The image provides a default directory, but adapters must ensure the
actual mounted path is writable by the runtime user and supports local kernel
locking. OSS binds the host `<workspace data root>/run/<bot>` directory at
`/run/memoh`, hiding the image directory; that host runtime directory must be on
a local filesystem, not NFS. The canonical root runtime can create `deps` inside
the mount. Cloud startup prepares the directory when `/run` is tmpfs. This does
not extend the UID/GID contract for arbitrary non-root custom images.

Control files can survive rootfs replacement through a runtime mount. Safety
depends on the whole-workspace lifetime proof and kernel locks, not deletion of
those files. macOS retains its existing data-root lock paths and `lockf` support.

Committed state records format version 2, installation identity, actual
payload/store paths, desired revision and publication identity. It has no
selectable previous installation. Entry points are validated inside the unique
payload; stable generated launchers preserve PATH integration. Metadata, Agent
Homes, cancellation markers and stable locks are outside payload collection.

## Isolated recipe protocol

Install and update scripts opt in through a leading comment covered by their
definition digests:

```sh
# memoh-storage-layout: isolated
```

This is not a new manifest field. New runners detect it before supplying isolated
paths; old runners ignore the comment and use the compatible version-directory
branch.

| Variable | Meaning |
| --- | --- |
| `MEMOH_DEP_HOME` | Persistent metadata directory for this dependency |
| `MEMOH_DEP_STORE` | This dependency's payload/cache directory |
| `MEMOH_DEP_INSTALL_DIR` | Unique candidate allocated by the runner |
| `MEMOH_DEP_STAGING` | This operation's staging directory |
| `MEMOH_DEP_VERSION` | Confirmed canonical version |
| `MEMOH_DEP_OPERATION_DIR` | Durable operation receipt directory |
| `MEMOH_DEP_RESULT` | Runner-owned result file outside the payload |
| `MEMOH_DEP_CANDIDATE` | Candidate executable for a frozen health probe |

The five shell recipes stage and rename complete trees. The other 27 generated
recipes may install directly into a unique unpublished candidate because venv,
Conda and wrappers embed absolute paths. Neither path may overwrite a published
installation. The recipe records its candidate; Server transaction checks and
finalization own publication of `current`.

The frozen health action verifies release and commands. Generated Node/Python
bundles also import declared modules. Composite resolutions retain small,
hash-checked package-version mappings in persistent `resolutions/`; these are
reconstruction metadata and never execution authority. Recipes retain checksum,
mirror and install-script restrictions.

An old frozen recipe keeps its original layout. Server does not repurpose
`MEMOH_DEP_HOME` to force it onto local storage. Same-version replacement of an
existing executable with an unsafe legacy recipe requires confirmation of an
isolated revision. Repair never upgrades that recipe silently.

## Transactions, recovery and collection

Scripts run through bridge stdin with result files, manifest time budgets,
per-dependency kernel locks and streamed output. Execution and filesystem
finalization share the lock. Linux runner, probes, finalization, payload collection
and launch leases use the local control namespace above. Linux uses `flock`;
macOS uses `lockf`. Lock files are not removed to reclaim ownership. Receipts,
state and the execution-window lifetime record remain persistent on `/data`;
database operation intent remains in PostgreSQL.

An accepted mutation atomically claims a database operation ID and its trusted
operation intent (action, target, publication and authorization identity). A
workspace receipt is evidence of execution, never the source of authorization.
Its immutable metadata must exactly match the database intent. Missing or
mismatched trusted intent is fenced under the kernel lock and marked failed,
preserving the previous usable payload and receipt evidence without granting
recovery authority. A new Manage confirmation is required. The receipt lives
outside the payload, and records completion even if its Server observer disconnects.
Recovery reads the stored frozen version probe without requiring the current
catalog's prerequisite graph. It checks ownership, exit status, candidate path,
exact version and actual files before reconciling
filesystem and database state. A canceled, never-started claim receives a persistent
cancellation marker before its claim is released. Unreachable workspaces retain
uncertain intent; timeout alone never proves that a script stopped.

A failed download, health probe or same-version replacement preserves the current
installation. An unpublished failed candidate can be cleaned after its completed
receipt proves safety. A committed replacement queues its retired published tree
for cleanup rather than exposing a rollback target.

Published payload collection requires a controlled **whole Linux workspace
restart** and a bridge-confirmed startup window before ordinary Exec or PTY work
has been admitted in that lifetime. Create/setup, API Start and lazy startup all
run Manager's maintenance boundary before declaring the workspace ready. The
persistent execution-window record prevents a bridge restart from presenting an
already-used lifetime as unused. Server restart, bridge-process restart, an empty process
scan or elapsed time is insufficient. Launch leases protect resolved commands and
their processes; launchers from an earlier workspace lifetime are rejected.

When startup proof is unavailable, including an old bridge or non-Linux workspace,
published payloads remain pending. This temporarily uses more disk than one
installation and requires a suitable maintenance window. Held leases, bootstrap
process references or incomplete process-access evidence also defer deletion.
Backends without the startup proof require operator-controlled maintenance for
published payloads. Cleanup follows only
explicitly recorded, validated paths, including a recorded legacy
`versions/<version>` path. It does not scan all old version folders or provide a
bulk-cleanup CLI. Store-root changes outside known namespaces require operator
review rather than deletion of unverified locations.

After isolated operations, cache cleanup runs under the dependency lock: files
older than seven days are eligible for removal, and caches above 512 MiB are
discarded. Persistent `resolutions/` and transaction metadata are not package caches.

Automatic recovery runs at workspace readiness and explicit execution preparation,
with persistent queue state and bounded backoff. It verifies the registered
managed payload even if a toolkit/PATH fallback exists. Surviving verified payloads
can have their launchers restored without another download. Missing payloads are
reinstalled from the same authorized version and stored recipe. Prerequisites
needing installation require their own targets. Missing authority, unavailable or
retired definitions and unsupported platforms require management attention.
`ResolveLauncher`, List and Preflight remain queries.

## Memoh API and UI

| Method | Path | Permission and behavior |
| --- | --- | --- |
| GET | `/workspace-dependencies` | Verified catalog metadata; optional `refresh=true` |
| GET | `/bots/{bot_id}/dependencies` | Workspace Read; observation and repair status |
| POST | `/bots/{bot_id}/dependencies/preflight` | Workspace Read; readiness query |
| POST | `/bots/{bot_id}/dependencies/check-updates` | Manage; upstream version checks |
| GET | `/bots/{bot_id}/dependencies/{dep_id}/script` | Manage; frozen script preview |
| POST | `/bots/{bot_id}/dependencies/{dep_id}/prepare` | Manage; exact installation target |
| POST | `/bots/{bot_id}/dependencies/{dep_id}/install` | Manage; confirmed target, SSE |
| POST | `/bots/{bot_id}/dependencies/{dep_id}/update` | Manage; confirmed target, SSE |
| POST | `/bots/{bot_id}/dependencies/{dep_id}/reinstall` | Manage; confirmed target, SSE |
| POST | `/bots/{bot_id}/dependencies/{dep_id}/repair/prepare` | Manage; recovery confirmation target |
| POST | `/bots/{bot_id}/dependencies/{dep_id}/repair/authorize` | Manage; reinstall and authorize recovery on success |
| POST | `/bots/{bot_id}/dependencies/{dep_id}/repair/retry` | Manage; retry the existing authorized target |
| POST | `/bots/{bot_id}/apps/prepare` | Manage; freeze App and dependency confirmations |

Dependency removal belongs to App removal and its shared-reference checks. The
removed dependency Rollback URL has no executable alias or redirect.

SSE `started` is emitted only after durable acceptance and carries the actual
operation ID and frozen definition revision. Disconnecting does not cancel
admitted work. List responses expose active and last completed operation IDs,
plus desired version, repair status, attempt count, next attempt and public error
code. The desired DTO excludes private payload paths and authorization actors.

Apps, dependency details and confirmation dialogs show one target and its recovery
state. Read-only members inspect status but cannot confirm scripts or retry.
HTTP/SSE failures use stable `workspace_dependency.*` codes localized by Web.
Icons retain immutable caching, sandbox CSP and nosniff without tokens in URLs.

## Upgrade and validation

Fresh installations use the canonical schema; deployed databases apply paired
incremental migrations. Legacy observations are not backfilled into recovery
authorizations. Drain old operations and deploy matching Server, bridge/image,
Web and SDK. Moving Linux locks from `/data` to `/run/memoh/deps` requires a full
workspace restart with the old scripts stopped before admitting new operations;
an idle new lock does not prove that an old-path owner has exited. Then
confirm isolated recipes before depending on ephemeral payload recovery. Follow
the [upgrade guide](../workspace-dependencies-upgrade.md).

Validation covers publication integrity, source/team isolation, frozen targets,
authorization and revocation races, failed transactions, same-version reinstall,
launch leases, rootfs-loss repair, controlled cleanup, API/UI permissions and
current generated SQL/OpenAPI/SDK. Actual checks, Cloud/E2B limits and outstanding
work belong in the [validation record](agent-cli-storage-validation.md). This
design does not declare those checks complete. Agent verification is not Human QA.
