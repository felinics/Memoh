-- name: GetBotPackageInstallation :one
SELECT id, team_id, bot_id, workspace_target_id, registry_id, package_id, revision, version,
       status, reason, available_revision, available_version, last_checked_at, last_error,
       release, installed_at, updated_at
FROM bot_package_installations
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND workspace_target_id = $2
  AND registry_id = $3
  AND package_id = $4
LIMIT 1;

-- name: GetBotPackageInstallationByID :one
SELECT id, team_id, bot_id, workspace_target_id, registry_id, package_id, revision, version,
       status, reason, available_revision, available_version, last_checked_at, last_error,
       release, installed_at, updated_at
FROM bot_package_installations
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND id = $2
LIMIT 1;

-- name: ListBotPackageInstallations :many
SELECT id, team_id, bot_id, workspace_target_id, registry_id, package_id, revision, version,
       status, reason, available_revision, available_version, last_checked_at, last_error,
       release, installed_at, updated_at
FROM bot_package_installations
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1
ORDER BY registry_id, package_id, workspace_target_id;

-- name: ListBotPackageInstallationsForTarget :many
SELECT id, team_id, bot_id, workspace_target_id, registry_id, package_id, revision, version,
       status, reason, available_revision, available_version, last_checked_at, last_error,
       release, installed_at, updated_at
FROM bot_package_installations
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND workspace_target_id = $2
ORDER BY registry_id, package_id;

-- name: UpsertBotPackageInstallation :one
INSERT INTO bot_package_installations (
  bot_id, workspace_target_id, registry_id, package_id, revision, version, status, reason, release
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (team_id, bot_id, workspace_target_id, registry_id, package_id)
DO UPDATE SET revision = EXCLUDED.revision,
              version = EXCLUDED.version,
              status = EXCLUDED.status,
              reason = CASE WHEN EXCLUDED.reason = 'user' THEN 'user' ELSE bot_package_installations.reason END,
              release = EXCLUDED.release,
              last_error = '',
              updated_at = now()
RETURNING id, team_id, bot_id, workspace_target_id, registry_id, package_id, revision, version,
          status, reason, available_revision, available_version, last_checked_at, last_error,
          release, installed_at, updated_at;

-- name: UpdateBotPackageInstallationStatus :one
UPDATE bot_package_installations
SET status = sqlc.arg(status),
    last_error = sqlc.arg(last_error),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(id)
RETURNING id, team_id, bot_id, workspace_target_id, registry_id, package_id, revision, version,
          status, reason, available_revision, available_version, last_checked_at, last_error,
          release, installed_at, updated_at;

-- name: UpdateBotPackageInstallationRelease :one
UPDATE bot_package_installations
SET revision = sqlc.arg(revision),
    version = sqlc.arg(version),
    release = sqlc.arg(release),
    available_revision = '',
    available_version = '',
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(id)
RETURNING id, team_id, bot_id, workspace_target_id, registry_id, package_id, revision, version,
          status, reason, available_revision, available_version, last_checked_at, last_error,
          release, installed_at, updated_at;

-- name: UpdateBotPackageInstallationCheck :one
UPDATE bot_package_installations
SET available_revision = sqlc.arg(available_revision),
    available_version = sqlc.arg(available_version),
    last_checked_at = sqlc.arg(last_checked_at),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(id)
RETURNING id, team_id, bot_id, workspace_target_id, registry_id, package_id, revision, version,
          status, reason, available_revision, available_version, last_checked_at, last_error,
          release, installed_at, updated_at;

-- name: DeleteBotPackageInstallation :one
DELETE FROM bot_package_installations
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND id = $2
RETURNING id, team_id, bot_id, workspace_target_id, registry_id, package_id, revision, version,
          status, reason, available_revision, available_version, last_checked_at, last_error,
          release, installed_at, updated_at;

-- name: ListPackageDependencyRefs :many
SELECT id, team_id, installation_id, dependency_id, created_at
FROM bot_package_dependency_refs
WHERE team_id = public.memoh_current_team_id()
  AND installation_id = $1
ORDER BY dependency_id;

-- name: ListPackageDependencyRefsForTarget :many
SELECT r.id, r.team_id, r.installation_id, r.dependency_id, r.created_at,
       i.registry_id, i.package_id
FROM bot_package_dependency_refs r
JOIN bot_package_installations i
  ON i.team_id = r.team_id AND i.id = r.installation_id
WHERE r.team_id = public.memoh_current_team_id()
  AND i.bot_id = $1
  AND i.workspace_target_id = $2
ORDER BY r.dependency_id, i.registry_id, i.package_id;

-- name: UpsertPackageDependencyRef :one
INSERT INTO bot_package_dependency_refs (installation_id, dependency_id)
VALUES ($1, $2)
ON CONFLICT (team_id, installation_id, dependency_id) DO UPDATE SET dependency_id = EXCLUDED.dependency_id
RETURNING id, team_id, installation_id, dependency_id, created_at;

-- name: DeletePackageDependencyRef :execrows
DELETE FROM bot_package_dependency_refs
WHERE team_id = public.memoh_current_team_id()
  AND installation_id = $1
  AND dependency_id = $2;

-- name: ListPackageConnectorRefs :many
SELECT id, team_id, installation_id, connector_type, connection_id, required, created_at, updated_at
FROM bot_package_connector_refs
WHERE team_id = public.memoh_current_team_id()
  AND installation_id = $1
ORDER BY connector_type;

-- name: ListPackageConnectorRefsForBot :many
SELECT r.id, r.team_id, r.installation_id, r.connector_type, r.connection_id, r.required,
       r.created_at, r.updated_at, i.registry_id, i.package_id, i.workspace_target_id
FROM bot_package_connector_refs r
JOIN bot_package_installations i
  ON i.team_id = r.team_id AND i.id = r.installation_id
WHERE r.team_id = public.memoh_current_team_id()
  AND i.bot_id = $1
ORDER BY r.connector_type, i.registry_id, i.package_id, i.workspace_target_id;

-- name: UpsertPackageConnectorRef :one
INSERT INTO bot_package_connector_refs (installation_id, connector_type, connection_id, required)
VALUES ($1, $2, $3, $4)
ON CONFLICT (team_id, installation_id, connector_type)
DO UPDATE SET connection_id = CASE WHEN EXCLUDED.connection_id <> '' THEN EXCLUDED.connection_id ELSE bot_package_connector_refs.connection_id END,
              required = EXCLUDED.required,
              updated_at = now()
RETURNING id, team_id, installation_id, connector_type, connection_id, required, created_at, updated_at;

-- name: SetPackageConnectorRefConnection :one
UPDATE bot_package_connector_refs
SET connection_id = sqlc.arg(connection_id),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND installation_id = sqlc.arg(installation_id)
  AND connector_type = sqlc.arg(connector_type)
RETURNING id, team_id, installation_id, connector_type, connection_id, required, created_at, updated_at;

-- name: ClearPackageConnectorRefConnection :execrows
UPDATE bot_package_connector_refs
SET connection_id = '',
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND connection_id = $1;

-- name: DeletePackageConnectorRef :execrows
DELETE FROM bot_package_connector_refs
WHERE team_id = public.memoh_current_team_id()
  AND installation_id = $1
  AND connector_type = $2;
