-- name: GetBotDependencyInstallation :one
SELECT id, team_id, bot_id, dependency_id, source, status,
       installed_version, latest_version, last_checked_at, last_error,
       manifest_digest, source_url, registry_id, definition_revision, operation_id, last_operation_id, operation_source_url, operation_registry_id, operation_definition_revision, operation_intent, created_at, updated_at
FROM bot_dependency_installations
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND dependency_id = $2
LIMIT 1;

-- name: ListBotDependencyInstallations :many
SELECT id, team_id, bot_id, dependency_id, source, status,
       installed_version, latest_version, last_checked_at, last_error,
       manifest_digest, source_url, registry_id, definition_revision, operation_id, last_operation_id, operation_source_url, operation_registry_id, operation_definition_revision, operation_intent, created_at, updated_at
FROM bot_dependency_installations
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
ORDER BY dependency_id;

-- name: ListBotDependencyInstallationsByStatus :many
SELECT id, team_id, bot_id, dependency_id, source, status,
       installed_version, latest_version, last_checked_at, last_error,
       manifest_digest, source_url, registry_id, definition_revision, operation_id, last_operation_id, operation_source_url, operation_registry_id, operation_definition_revision, operation_intent, created_at, updated_at
FROM bot_dependency_installations
WHERE team_id = public.memoh_current_team_id()
  AND status = $1
ORDER BY bot_id, dependency_id;

-- name: ListStaleBotDependencyOperations :many
SELECT id, team_id, bot_id, dependency_id, source, status,
       installed_version, latest_version, last_checked_at, last_error,
       manifest_digest, source_url, registry_id, definition_revision, operation_id, last_operation_id, operation_source_url, operation_registry_id, operation_definition_revision, operation_intent, created_at, updated_at
FROM bot_dependency_installations
WHERE team_id = public.memoh_current_team_id()
  AND status IN ('installing', 'updating', 'removing')
  AND updated_at < now() - make_interval(secs => sqlc.arg(older_than_seconds)::double precision)
ORDER BY updated_at, id;

-- name: UpsertBotDependencyInstallationIntent :one
INSERT INTO bot_dependency_installations (
  bot_id, dependency_id, source, status,
  installed_version, manifest_digest, source_url, registry_id, definition_revision
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (team_id, bot_id, dependency_id)
DO UPDATE SET source = EXCLUDED.source,
              last_error = '',
              status = EXCLUDED.status,
              installed_version = EXCLUDED.installed_version,
              manifest_digest = EXCLUDED.manifest_digest,
              source_url = EXCLUDED.source_url, registry_id = EXCLUDED.registry_id,
              definition_revision = EXCLUDED.definition_revision,
              updated_at = now()
WHERE bot_dependency_installations.operation_id = ''
RETURNING id, team_id, bot_id, dependency_id, source, status,
          installed_version, latest_version, last_checked_at, last_error,
          manifest_digest, source_url, registry_id, definition_revision, operation_id, last_operation_id, operation_source_url, operation_registry_id, operation_definition_revision, operation_intent, created_at, updated_at;

-- name: UpdateBotDependencyInstallationStatus :one
UPDATE bot_dependency_installations
SET status = sqlc.arg(status),
    last_error = sqlc.arg(last_error),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND dependency_id = sqlc.arg(dependency_id)
  AND operation_id = ''
RETURNING id, team_id, bot_id, dependency_id, source, status,
          installed_version, latest_version, last_checked_at, last_error,
          manifest_digest, source_url, registry_id, definition_revision, operation_id, last_operation_id, operation_source_url, operation_registry_id, operation_definition_revision, operation_intent, created_at, updated_at;

-- name: UpdateBotDependencyInstallationObserved :one
UPDATE bot_dependency_installations
SET source = COALESCE(sqlc.narg(source)::text, source),
    installed_version = COALESCE(sqlc.narg(installed_version)::text, installed_version),
    latest_version = COALESCE(sqlc.narg(latest_version)::text, latest_version),
    last_checked_at = COALESCE(sqlc.narg(last_checked_at)::timestamptz, last_checked_at),
    last_error = COALESCE(sqlc.narg(last_error)::text, last_error),
    manifest_digest = COALESCE(sqlc.narg(manifest_digest)::text, manifest_digest),
    source_url = COALESCE(sqlc.narg(source_url)::text, source_url),
    registry_id = COALESCE(sqlc.narg(registry_id)::text, registry_id),
    definition_revision = COALESCE(sqlc.narg(definition_revision)::text, definition_revision),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND dependency_id = sqlc.arg(dependency_id)
  AND operation_id = ''
RETURNING id, team_id, bot_id, dependency_id, source, status,
          installed_version, latest_version, last_checked_at, last_error,
          manifest_digest, source_url, registry_id, definition_revision, operation_id, last_operation_id, operation_source_url, operation_registry_id, operation_definition_revision, operation_intent, created_at, updated_at;

-- name: DeleteBotDependencyInstallation :execrows
DELETE FROM bot_dependency_installations
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND dependency_id = $2
  AND operation_id = '';

-- name: ClaimBotDependencyOperation :one
-- All claims and successful finishes lock desired before installation. The
-- aggregate preserves one input row when no target has been authorized yet.
WITH target_lock AS MATERIALIZED (
  SELECT d.desired_revision FROM bot_dependency_desired_installations d
  WHERE d.team_id = public.memoh_current_team_id() AND d.bot_id = $1 AND d.dependency_id = $2
  FOR UPDATE
), target_lock_count AS MATERIALIZED (
  SELECT count(*) FROM target_lock
), claimed AS (
INSERT INTO bot_dependency_installations (
  bot_id, dependency_id, source, status,
  installed_version, manifest_digest, source_url, registry_id, definition_revision, operation_id, operation_source_url, operation_registry_id, operation_definition_revision, operation_intent
)
VALUES ((SELECT $1::uuid FROM target_lock_count), $2, $3, $4, $5, $6, $7, $8, $9, $10, $7, $8, $9, sqlc.narg(operation_intent)::jsonb)
ON CONFLICT (team_id, bot_id, dependency_id)
DO UPDATE SET status = EXCLUDED.status,
              last_error = '',
              operation_id = EXCLUDED.operation_id,
              operation_source_url = EXCLUDED.operation_source_url,
              operation_registry_id = EXCLUDED.operation_registry_id,
              operation_definition_revision = EXCLUDED.operation_definition_revision,
              operation_intent = EXCLUDED.operation_intent,
              updated_at = now()
WHERE bot_dependency_installations.status NOT IN ('installing', 'updating', 'removing')
RETURNING id, team_id, bot_id, dependency_id, source, status,
          installed_version, latest_version, last_checked_at, last_error,
          manifest_digest, source_url, registry_id, definition_revision, operation_id, last_operation_id, operation_source_url, operation_registry_id, operation_definition_revision, operation_intent, created_at, updated_at
), invalidated AS (
  UPDATE bot_dependency_desired_installations d
  SET desired_revision = c.operation_id, repair_status = 'ready', repair_operation_id = '',
      repair_attempts = 0, repair_next_attempt_at = NULL, repair_last_error_code = '', updated_at = now()
  FROM claimed c
  WHERE d.team_id = c.team_id AND d.bot_id = c.bot_id AND d.dependency_id = c.dependency_id AND c.status <> 'removing'
  RETURNING d.bot_id
), revoked AS (
  DELETE FROM bot_dependency_desired_installations d USING claimed c
  WHERE d.team_id = c.team_id AND d.bot_id = c.bot_id AND d.dependency_id = c.dependency_id AND c.status = 'removing'
  RETURNING d.*
), audited AS (
  INSERT INTO bot_dependency_authorization_events (bot_id, dependency_id, operation_id, action, actor, version, source_url, registry_id, definition_revision, manifest_digest)
  SELECT r.bot_id, r.dependency_id, c.operation_id, 'revoke', sqlc.arg(actor)::text, r.version, r.source_url, r.registry_id, r.definition_revision, r.manifest_digest
  FROM revoked r JOIN claimed c ON r.bot_id = c.bot_id AND r.dependency_id = c.dependency_id
  ON CONFLICT DO NOTHING
)
SELECT * FROM claimed;

-- name: FinishBotDependencyOperation :one
UPDATE bot_dependency_installations
SET source = sqlc.arg(source),
    status = sqlc.arg(status),
    installed_version = sqlc.arg(installed_version),
    latest_version = sqlc.arg(latest_version),
    last_checked_at = sqlc.narg(last_checked_at)::timestamptz,
    last_error = sqlc.arg(last_error),
    manifest_digest = sqlc.arg(manifest_digest),
    source_url = sqlc.arg(source_url),
    registry_id = sqlc.arg(registry_id),
    definition_revision = sqlc.arg(definition_revision),
    last_operation_id = operation_id, operation_id = '',
    operation_source_url = '', operation_registry_id = '', operation_definition_revision = '', operation_intent = NULL,
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND dependency_id = sqlc.arg(dependency_id)
  AND operation_id = sqlc.arg(operation_id) AND operation_id <> ''
RETURNING id, team_id, bot_id, dependency_id, source, status,
          installed_version, latest_version, last_checked_at, last_error,
          manifest_digest, source_url, registry_id, definition_revision, operation_id, last_operation_id, operation_source_url, operation_registry_id, operation_definition_revision, operation_intent, created_at, updated_at;

-- name: DeleteBotDependencyOperation :one
DELETE FROM bot_dependency_installations
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND dependency_id = sqlc.arg(dependency_id)
  AND operation_id = sqlc.arg(operation_id) AND operation_id <> ''
RETURNING id, team_id, bot_id, dependency_id, source, status,
          installed_version, latest_version, last_checked_at, last_error,
          manifest_digest, source_url, registry_id, definition_revision, operation_id, last_operation_id, operation_source_url, operation_registry_id, operation_definition_revision, operation_intent, created_at, updated_at;

-- name: EnrollLegacyDependencyOperationEpoch :one
-- The first observation is a lifetime fence, not a heartbeat or new approval.
-- Preserve it across Server restarts and do not change stale-operation timing.
UPDATE bot_dependency_installations
SET operation_intent = CASE
    WHEN COALESCE(operation_intent ->> 'control_migration_epoch', '') <> '' THEN operation_intent
    ELSE COALESCE(operation_intent, '{}'::jsonb) || jsonb_build_object('control_migration_epoch', sqlc.arg(epoch)::text)
END
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id) AND dependency_id = sqlc.arg(dependency_id)
  AND operation_id = sqlc.arg(operation_id)
  AND status IN ('installing', 'updating', 'removing')
RETURNING COALESCE(operation_intent ->> 'control_migration_epoch', '')::text AS epoch;
