# Upgrading to managed workspace dependencies

The workspace image now supplies Node.js, Python, uv, the display stack, and
runtime scripts. Codex and Claude Code are managed per workspace under
`/data/.memoh/deps`; they are no longer installed in the image. The new Server
discovers an existing supported CLI before requiring a managed installation.
A chat message or a device-code login attempt does not authorize installation:
a user with the Bot's Manage permission must confirm installation in workspace
dependency settings.

## Server and image compatibility

| Server | Workspace image | Upgrade behavior |
| --- | --- | --- |
| Previous Server with the v3 workspace contract | Previous matching image with bundled CLIs | Keep this pair for rollback. |
| New Server with dependency management | Previous image with supported bundled CLIs | Supported transition; existing CLIs remain usable, including with a cold catalog cache or without Supermarket access. |
| New Server with dependency management | New baseline image | Install the required managed CLIs before sending Agent work to the recreated workspace. |
| Previous Server with the v3 workspace contract | New baseline image | Unsupported. Workspace initialization fails before the Agent starts. |

The previous Server requires `/opt/memoh/workspace-contract.json` **and**
executable toolkit launchers for Codex and Claude Code. Restoring only the JSON
file does not make the baseline image compatible. The new Server no longer
uses this image-level check; it validates a dependency at its point of use.

## Upgrade an existing installation

1. Schedule a maintenance window and stop admitting new Agent work while changing
   Server or recreating workspace containers. Record the running Server release,
   configuration, and the image actually used by each workspace. Resolve image
   references to immutable digests, retain those images in the backend's image
   store or a reachable registry, and take a consistent database and workspace
   data backup. Retain the entire data volume, including `.memoh`, credentials,
   and managed dependency state.
2. Pin `[container].default_image` and any per-workspace image choices to the old
   image digest. For example, use
   `memohai/workspace@sha256:<recorded-old-image-digest>`. Changing the default
   does not replace an existing workspace's image. Do not rely on
   `debian`, `debian-dev`, `debian-latest`, `latest`, or a partial version tag:
   these can move independently of the running Server. `if_not_present` is a
   cache policy, not a compatibility guarantee.
3. Apply the release's database migrations and upgrade Server, Channel, and Web
   as a matching release while retaining the old workspace images. Check that an
   existing Codex or Claude Code Bot can start with its supported bundled CLI.
   An installed CLI outside the supported version range must be updated before
   the Agent can run.
4. While the old image is still available, have a Manage-capable user open each
   relevant workspace target's dependency settings and confirm installation of
   the required Codex or Claude Code version. Wait for successful installation, verify the managed executable
   and its version, and start a fresh Agent session. Dependencies are stored per
   workspace target; installation for one Bot or target does not prepare others.
5. Only after those checks, select the new baseline image by immutable digest
   and recreate the workspace through the normal image-change flow, preserving
   its existing data volume. Verify a fresh Agent session, credentials, command
   execution, and any enabled display features before reopening traffic. Keep
   the old image and backups until this validation is complete.

A workspace recreated with the baseline image before step 4 has no bundled
Codex or Claude Code to fall back to. Its first Agent turn will report a missing
dependency until an administrator completes installation; it cannot finish an
installation merely by retrying chat. Downloading the catalog, the recipe, and
upstream runtime artifacts requires network access unless they are already
available through the deployment's approved distribution path.

## Offline installations

Keep the previous image and its supported CLIs pinned if Supermarket or upstream
download hosts are unavailable. A catalog outage does not require replacing or
removing an already usable CLI. Do not switch an offline workspace to the new
baseline image until the required managed dependencies have been installed and
verified in that same persisted workspace data volume. A new baseline image by
itself does not include an offline Agent installer or a cached recipe catalog.

Configure maintenance in the Server configuration:

```toml
[workspace_dependencies]
offline = true
catalog_refresh_interval_seconds = 600
update_check_interval_seconds = 86400
reap_interval_seconds = 60
discovery_cache_ttl_seconds = 600
```

The interval values above are the defaults; use positive seconds to override them.
Offline mode disables registry refresh and automatic upstream update checks while
preserving cached definitions and discovery of installed commands. It is not a
network sandbox for explicitly confirmed scripts: a recipe may still download
software, and an uncached definition must be acquired before entering offline mode.

The optional `[workspace_dependencies.script_env]` table accepts only
`NODEJS_MIRROR`, `UV_RELEASES_URL`, `NPM_MIRROR`, and `UV_PYTHON_INSTALL_MIRROR`.
These values are supplied explicitly to recipes; unrelated Server environment
variables are not inherited. Node.js and uv archives downloaded from a mirror
still require checksum verification against the official HTTPS release source,
so changing an archive mirror alone does not provide fully offline installation.

## Rollback

Pause Agent work and wait for dependency operations to finish before rollback.
Restore the recorded Server/Channel/Web release and the matching **previous
workspace image digest** together; do not restart the previous Server against
the baseline image. Recreate affected workspaces with the retained data volume
and verify initialization and a fresh Agent session before resuming traffic.
An old Server does not manage `.memoh/deps`; keep that directory intact so a
subsequent upgrade can recover the managed installations.

Follow the release's database rollback procedure, or restore the consistent
pre-upgrade database and workspace backups as a pair. Dependency installation
records alone are not a filesystem backup, and reverting a database migration
does not undo scripts that ran in a workspace. Restoring backups also discards
changes made after the backup; choose that recovery point deliberately.

These steps cover the workspace-image boundary. They do not assert compatibility
between unrelated Server versions, bridge protocols, or database migrations.

### Recovering interrupted operations

A reachable workspace preserves each accepted mutation's receipt outside the
removable dependency directory. Recovery checks the workspace kernel lock and
recorded script exit before completing database state. An abandoned operation
that never started receives a permanent cancellation marker before its database
claim is released. Do not delete these markers: a paused older Server must still
be prevented from executing the canceled operation.

Stopped or unreachable workspaces retain their in-progress intent until recovery
can reach the workspace. Pre-protocol rows without an operation id and legacy
lock directories require explicit operator recovery after the old Server and
workspace processes have stopped; they are never reclaimed by elapsed time alone.
