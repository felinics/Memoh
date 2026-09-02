-- name: GetBotDependencyInstallation :one
SELECT id, team_id, bot_id, workspace_target_id, dependency_id, source, status,
       installed_version, latest_version, last_checked_at, last_error,
       manifest_digest, created_at, updated_at
FROM bot_dependency_installations
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND workspace_target_id = $2
  AND dependency_id = $3
LIMIT 1;

-- name: ListBotDependencyInstallationsForTarget :many
SELECT id, team_id, bot_id, workspace_target_id, dependency_id, source, status,
       installed_version, latest_version, last_checked_at, last_error,
       manifest_digest, created_at, updated_at
FROM bot_dependency_installations
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND workspace_target_id = $2
ORDER BY dependency_id;

-- name: ListBotDependencyInstallations :many
SELECT id, team_id, bot_id, workspace_target_id, dependency_id, source, status,
       installed_version, latest_version, last_checked_at, last_error,
       manifest_digest, created_at, updated_at
FROM bot_dependency_installations
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
ORDER BY workspace_target_id, dependency_id;

-- name: ListBotDependencyInstallationsByStatus :many
SELECT id, team_id, bot_id, workspace_target_id, dependency_id, source, status,
       installed_version, latest_version, last_checked_at, last_error,
       manifest_digest, created_at, updated_at
FROM bot_dependency_installations
WHERE team_id = public.memoh_current_team_id()
  AND status = $1
ORDER BY bot_id, workspace_target_id, dependency_id;

-- name: ListStaleBotDependencyOperations :many
SELECT id, team_id, bot_id, workspace_target_id, dependency_id, source, status,
       installed_version, latest_version, last_checked_at, last_error,
       manifest_digest, created_at, updated_at
FROM bot_dependency_installations
WHERE team_id = public.memoh_current_team_id()
  AND status IN ('installing', 'updating', 'removing')
  AND updated_at < now() - make_interval(secs => sqlc.arg(older_than_seconds)::double precision)
ORDER BY updated_at, id;

-- name: UpsertBotDependencyInstallationIntent :one
INSERT INTO bot_dependency_installations (
  bot_id, workspace_target_id, dependency_id, source, status,
  installed_version, manifest_digest
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (team_id, bot_id, workspace_target_id, dependency_id)
DO UPDATE SET source = EXCLUDED.source,
              status = EXCLUDED.status,
              installed_version = EXCLUDED.installed_version,
              manifest_digest = EXCLUDED.manifest_digest,
              updated_at = now()
RETURNING id, team_id, bot_id, workspace_target_id, dependency_id, source, status,
          installed_version, latest_version, last_checked_at, last_error,
          manifest_digest, created_at, updated_at;

-- name: UpdateBotDependencyInstallationStatus :one
UPDATE bot_dependency_installations
SET status = sqlc.arg(status),
    last_error = sqlc.arg(last_error),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND workspace_target_id = sqlc.arg(workspace_target_id)
  AND dependency_id = sqlc.arg(dependency_id)
RETURNING id, team_id, bot_id, workspace_target_id, dependency_id, source, status,
          installed_version, latest_version, last_checked_at, last_error,
          manifest_digest, created_at, updated_at;

-- name: UpdateBotDependencyInstallationObserved :one
UPDATE bot_dependency_installations
SET source = COALESCE(sqlc.narg(source)::text, source),
    installed_version = COALESCE(sqlc.narg(installed_version)::text, installed_version),
    latest_version = COALESCE(sqlc.narg(latest_version)::text, latest_version),
    last_checked_at = COALESCE(sqlc.narg(last_checked_at)::timestamptz, last_checked_at),
    last_error = COALESCE(sqlc.narg(last_error)::text, last_error),
    manifest_digest = COALESCE(sqlc.narg(manifest_digest)::text, manifest_digest),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND workspace_target_id = sqlc.arg(workspace_target_id)
  AND dependency_id = sqlc.arg(dependency_id)
RETURNING id, team_id, bot_id, workspace_target_id, dependency_id, source, status,
          installed_version, latest_version, last_checked_at, last_error,
          manifest_digest, created_at, updated_at;

-- name: DeleteBotDependencyInstallation :execrows
DELETE FROM bot_dependency_installations
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND workspace_target_id = $2
  AND dependency_id = $3;
