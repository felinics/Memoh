# Workspace dependencies from Supermarket

## Ownership

Supermarket publishes the official `memoh` dependency registry. Memoh contains the
manifest parser, verified artifact loader, discovery, script runner, installation
state machine, and runtime integration. It does not embed dependency definitions or
installation scripts. Adding a CLI or correcting its recipe is a registry release,
not a Memoh release. Adding an Agent runtime driver still requires Memoh code.

The first release supports the official registry only and includes Codex, Claude
Code, Node.js, Python, and uv. Skill Packages remain a separate resource and retain
their existing installer and `postinstall` behavior.

## Definition protocol

A dependency directory in Supermarket contains `dependency.yaml`, its referenced
POSIX sh scripts, and an optional icon. The version 1 manifest describes identity,
localized display text, category, image baseline or managed source, prerequisites,
provided commands, platform support, optional software version pin, and per-action
timeouts. Unknown fields and unsupported schema versions are rejected.

Supermarket publishes four HTTP surfaces:

| Method | Path | Meaning |
| --- | --- | --- |
| GET | `/api/dependencies` | Current catalog; search, registry/category filtering and pagination |
| GET | `/api/registries/memoh/dependencies/{id}` | Current dependency descriptor |
| GET | `/api/registries/memoh/dependencies/{id}/releases/{revision}` | Immutable release document |
| GET | `/api/artifacts/dependency/{digest}` | Immutable compressed definition artifact |

Dependency snapshots and their reviewed `dependencies.lock.json` are independent of
Skill snapshots. The shared blob backends, immutable writes and conditional state
updates are reused. Publishing one resource cannot overwrite the other's pointer.

There are three distinct identities:

- **Definition revision:** SHA-256 of the exact immutable release JSON bytes.
- **Artifact digest:** SHA-256 of the compressed archive bytes. The descriptor also
  carries compressed, uncompressed, serialized-tar and file-count budgets.
- **Software version:** the version actually installed by the script, recorded from
  its result rather than assumed from the request.

The manifest digest additionally frames the sorted manifest/script filenames,
lengths and contents. Both implementations use `name + NUL + byte length + NUL +
contents`. Icons have their own digest. Memoh checks the release, archive, manifest
projection, script digest and icon contents before making a definition usable.
Archives are parsed in memory; links, unsafe paths, duplicate files, undeclared
files, malformed metadata and budget violations are rejected before execution.

The reference publisher enforces valid prerequisite references and rejects cycles.
Version 1 does not introduce a recursive dependency solver: prerequisites retain the
existing behavior of the workspace image and installation scripts.

## Latest policy and immutable operations

Every scripted management operation resolves the latest published definition:
install, update, reinstall, remove, manual update checks and background update
checks. A software version request is independent of that choice. In particular,
requesting an older CLI version still uses the current installation recipe.

Preparation freezes one revision for the operation. Script preview returns
`definition_revision`; the corresponding operation request carries that revision.
A refresh or publication between preview and execution cannot swap the script.
A new operation or an explicit retry resolves latest again unless the user is
confirming that prepared preview. Reinstall's remove/install pair shares one
snapshot and one dependency lock.

Same-schema recipe changes must preserve the managed directory and result contract,
including the ability to remove installations produced by prior recipe revisions.
New scripts never replace a running operation's in-memory definition.

Rollback is different: it executes Memoh's fixed symlink-switch operation, not a
registry rollback script. The previous software version, entrypoints and recorded
publication identity are restored together. No registry request is needed.

## Persistent cache and availability

Memoh uses the configured `supermarket.base_url` and accepts only `memoh` dependency
releases from that origin. Downloads and redirects stay on the configured origin;
artifact URLs are constructed from verified digests, not arbitrary metadata URLs.
Compiled runtime requirements bind official dependency IDs to launcher commands.

PostgreSQL stores immutable release/archive bytes separately from installation
intent. The cache key includes normalized source URL, registry, dependency ID and
revision. Tables use the existing connection-bound team RLS. Catalog pointer updates
use a generation compare-and-swap so a delayed Server cannot overwrite a newer
snapshot published by another Server. Definitions needed by installed or historical
operations remain available in the persistent cache.

- The Server starts without connecting to Supermarket. A nonblocking worker refreshes
  the catalog at startup and every ten minutes.
- Catalog reads use a fresh cached snapshot; an explicit refresh or operation fetches
  current metadata. Unchanged release blobs are reused by digest.
- Availability failures use the last verified snapshot and mark the response stale.
  With no cache, catalog-dependent operations return a retryable error.
- Invalid content is rejected and cannot replace the cache. It is not silently
  treated as an availability failure.
- Retired entries disappear from the installable catalog. Cached definitions remain
  available to manage existing records; a new install of a retired entry is refused.
- An installed Agent uses cached definitions and workspace discovery. A missing CLI
  can enqueue a background installation without blocking its turn on catalog I/O.
- Script caching is not binary caching. Reinstalling software may still require npm,
  GitHub or the relevant upstream download service.

## Workspace layout and execution

Managed dependencies live under `<data root>/.memoh/deps/<id>/versions/<version>`.
`current` identifies the active version, `state.json` records its publication and
entrypoints, and generated shims in `.memoh/deps/bin` precede the image toolkit on
PATH. Native workspaces use `/data`; remote targets supply their own data root.

The image keeps Node.js, Python, uv, display tools and the bridge contract paths.
Node.js/Python/uv installations add managed overlays; removal restores the image
baseline. Codex and Claude Code are downloaded per workspace and are absent from the
image. The old workspace-contract JSON gate is removed.

Scripts run through bridge stdin with the shared prelude, a result file, timeouts,
per-dependency locks and streamed stdout/stderr. Installs stage a complete version
before switching `current`. Discovery reconciles installation intent with workspace
state, including missing files and copies restored by workspace snapshots. Its cache
is invalidated by bridge resets and by the definition fingerprint.

Installation records distinguish current software version from definition revision.
Script operation logs and SSE receipts identify the revision actually used. Private
errors stay in logs; HTTP and SSE failures expose stable `workspace_dependency.*`
codes, and the UI localizes them. Discovery and stored operation errors expose codes
rather than internal causes.

## Memoh API and UI

Existing `/bots/{bot_id}/dependencies` actions remain. Install/update/reinstall and
remove accept an optional `definition_revision`; install-like actions also accept
`version`. The script endpoint accepts an optional prepared revision and returns the
same revision with the preview. Started/done events include definition revision.

`GET /workspace-dependencies/catalog` serves verified remote metadata, and
`refresh=true` explicitly retries the source. Bot listing has the same refresh knob.
Responses include cache freshness, localized names/descriptions, publication
identity and digest-addressed cached icon URLs. Icons are served with immutable
caching, a sandbox CSP and nosniff; no authentication token is embedded in image URLs.

The existing Supermarket Dependencies tab, installed dependency rows, confirm dialogs,
background progress store, Agent enable preflight and missing-dependency chat card
remain the user flow. A cached/offline catalog has an actionable retry notice. A
new dependency's presentation does not require a frontend ID-to-text or ID-to-icon
mapping. The script dialog is the diagnostic surface for revision and execution
information; these details do not become extra root-page controls.

## Initialization and validation

This replaces an unmerged experimental implementation. Only fresh initialization is
supported; there is no migration of old experimental dependency records. The stack's
migration and canonical schema are updated together. Existing local development
volumes are retained as backups while validation uses new volumes.

Required validation includes:

- Deterministic publication, reviewed locks, immutable history, corrupt content
  rejection, bounded archives and concurrent publisher behavior.
- Producer-generated wire fixtures consumed by Go, including manifest/script and
  archive digests.
- Persistent cache across restarts, source/team isolation, generation CAS, absent
  upstream with/without cache, retirement and runtime command validation.
- Preview/execute revision consistency while a newer definition is published.
- Real HTTP installation, software version selection, update, reinstall, removal,
  overlays and rollback for the five official dependencies.
- Publishing an additional test dependency without changing Go/TS and installing it
  through Memoh; current-definition behavior for subsequent operations.
- Fresh PostgreSQL initialization and incremental down/up, Go tests/race/lint,
  frontend tests/type checks/UI guard, Supermarket tests/type checks/build, and all
  six stacked PR CI summaries.

Human UI verification is disclosed separately in PR descriptions; automated checks
and agent-run API verification do not remove the No human QA marker.
