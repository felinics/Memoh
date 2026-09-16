-- name: GetDesiredDependencyInstallation :one
SELECT * FROM bot_dependency_desired_installations
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1 AND dependency_id = $2;

-- name: ListDesiredDependencyInstallations :many
SELECT * FROM bot_dependency_desired_installations
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1 ORDER BY dependency_id;

-- name: ListDesiredDependencyRepairPage :many
SELECT * FROM bot_dependency_desired_installations
WHERE team_id = public.memoh_current_team_id()
  AND (bot_id, dependency_id) > (sqlc.arg(after_bot_id)::uuid, sqlc.arg(after_dependency_id)::text)
ORDER BY bot_id, dependency_id LIMIT sqlc.arg(page_size);

-- name: QueueDesiredDependencyRepair :one
UPDATE bot_dependency_desired_installations
SET repair_status = 'queued', repair_operation_id = sqlc.arg(operation_id),
    repair_attempts = CASE WHEN sqlc.arg(manual_retry)::boolean THEN 0 ELSE repair_attempts END,
    repair_next_attempt_at = NULL, repair_last_error_code = '', updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id) AND dependency_id = sqlc.arg(dependency_id)
  AND desired_revision = sqlc.arg(desired_revision)
  AND (repair_status = 'ready'
    OR (repair_status = 'backoff' AND repair_next_attempt_at <= sqlc.arg(now_at)::timestamptz)
    OR (sqlc.arg(manual_retry)::boolean AND repair_status IN ('backoff','manual_required')))
RETURNING *;

-- name: SetDesiredDependencyRepairResult :one
UPDATE bot_dependency_desired_installations
SET repair_status = sqlc.arg(repair_status),
    repair_operation_id = '', repair_attempts = sqlc.arg(repair_attempts),
    repair_next_attempt_at = sqlc.narg(repair_next_attempt_at)::timestamptz,
    repair_last_error_code = sqlc.arg(repair_last_error_code), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id) AND dependency_id = sqlc.arg(dependency_id)
  AND desired_revision = sqlc.arg(desired_revision)
  AND repair_operation_id = sqlc.arg(operation_id)
  AND repair_status = sqlc.arg(expected_repair_status)
RETURNING *;

-- name: RevokeDesiredDependencyInstallation :execrows
WITH removed AS (
  DELETE FROM bot_dependency_desired_installations d
  WHERE d.team_id = public.memoh_current_team_id() AND d.bot_id = sqlc.arg(bot_id) AND d.dependency_id = sqlc.arg(dependency_id)
    AND EXISTS (SELECT 1 FROM bot_dependency_installations i WHERE i.team_id = d.team_id AND i.bot_id = d.bot_id AND i.dependency_id = d.dependency_id AND i.operation_id = sqlc.arg(operation_id) AND i.status = 'removing')
  RETURNING *
)
INSERT INTO bot_dependency_authorization_events (bot_id, dependency_id, operation_id, action, actor, version, source_url, registry_id, definition_revision, manifest_digest)
SELECT bot_id, dependency_id, sqlc.arg(operation_id), 'revoke', sqlc.arg(actor), version, source_url, registry_id, definition_revision, manifest_digest FROM removed
ON CONFLICT DO NOTHING;

-- name: ClaimDesiredDependencyRepair :one
WITH target AS (
  SELECT d.* FROM bot_dependency_desired_installations d
  WHERE d.team_id = public.memoh_current_team_id() AND d.bot_id = sqlc.arg(bot_id) AND d.dependency_id = sqlc.arg(dependency_id)
    AND d.desired_revision = sqlc.arg(desired_revision) AND d.repair_status = 'queued' AND d.repair_operation_id = sqlc.arg(operation_id)
  FOR UPDATE
), claimed AS (
  INSERT INTO bot_dependency_installations (bot_id, dependency_id, source, status, installed_version, manifest_digest, source_url, registry_id, definition_revision, operation_id, operation_source_url, operation_registry_id, operation_definition_revision, operation_intent)
  SELECT bot_id, dependency_id, 'managed', 'installing', version, manifest_digest, source_url, registry_id, definition_revision, sqlc.arg(operation_id), source_url, registry_id, definition_revision, sqlc.narg(operation_intent)::jsonb FROM target
  ON CONFLICT (team_id, bot_id, dependency_id) DO UPDATE
  SET status = 'installing', operation_id = EXCLUDED.operation_id,
      operation_source_url = EXCLUDED.operation_source_url, operation_registry_id = EXCLUDED.operation_registry_id, operation_definition_revision = EXCLUDED.operation_definition_revision, operation_intent = EXCLUDED.operation_intent,
      last_error = '', updated_at = now()
  WHERE bot_dependency_installations.status NOT IN ('installing','updating','removing')
  RETURNING *
), started AS (
  UPDATE bot_dependency_desired_installations d SET repair_status = 'installing', repair_attempts = repair_attempts + 1, updated_at = now()
  WHERE d.team_id = public.memoh_current_team_id() AND d.bot_id = sqlc.arg(bot_id) AND d.dependency_id = sqlc.arg(dependency_id)
    AND EXISTS (SELECT 1 FROM claimed) RETURNING d.bot_id
)
SELECT claimed.* FROM claimed, started;

-- name: InvalidateQueuedDesiredDependencyRepair :execrows
-- An admitted management operation supersedes older queued recovery without
-- changing the previous successfully installed target or its authorization.
UPDATE bot_dependency_desired_installations d
SET desired_revision = sqlc.arg(operation_id), repair_status = 'ready', repair_operation_id = '',
    repair_attempts = 0, repair_next_attempt_at = NULL, repair_last_error_code = '', updated_at = now()
WHERE d.team_id = public.memoh_current_team_id() AND d.bot_id = sqlc.arg(bot_id) AND d.dependency_id = sqlc.arg(dependency_id)
  AND EXISTS (SELECT 1 FROM bot_dependency_installations i WHERE i.team_id = d.team_id AND i.bot_id = d.bot_id AND i.dependency_id = d.dependency_id AND i.operation_id = sqlc.arg(operation_id));

-- name: FinishAuthorizedDependencyOperation :one
-- Match repair claims: acquire the optional desired row before installation.
WITH target_lock AS MATERIALIZED (
  SELECT d.desired_revision FROM bot_dependency_desired_installations d
  WHERE d.team_id = public.memoh_current_team_id() AND d.bot_id = sqlc.arg(bot_id) AND d.dependency_id = sqlc.arg(dependency_id)
  FOR UPDATE
), owned AS (
  UPDATE bot_dependency_installations i
  SET source = 'managed', status = 'installed', installed_version = sqlc.arg(version), manifest_digest = sqlc.arg(manifest_digest),
      source_url = sqlc.arg(source_url), registry_id = sqlc.arg(registry_id), definition_revision = sqlc.arg(definition_revision),
      last_operation_id = i.operation_id, operation_id = '', operation_source_url = '', operation_registry_id = '', operation_definition_revision = '', operation_intent = NULL, last_error = '', updated_at = now()
  WHERE i.team_id = public.memoh_current_team_id() AND i.bot_id = sqlc.arg(bot_id) AND i.dependency_id = sqlc.arg(dependency_id)
    AND i.operation_id = sqlc.arg(operation_id) AND i.operation_id <> ''
    AND (SELECT count(*) FROM target_lock) >= 0 RETURNING i.*
), desired AS (
  INSERT INTO bot_dependency_desired_installations (bot_id, dependency_id, desired_revision, version, source_url, registry_id, definition_revision, manifest_digest,
      authorized_by_operation_id, authorized_by_actor, platform_os, platform_arch, platform_libc, entrypoints, installation_id, payload_path, store_root)
  SELECT bot_id, dependency_id, sqlc.arg(operation_id), installed_version, source_url, registry_id, definition_revision, manifest_digest,
      sqlc.arg(operation_id), sqlc.arg(actor), sqlc.arg(platform_os), sqlc.arg(platform_arch), sqlc.arg(platform_libc), sqlc.arg(entrypoints)::jsonb, sqlc.arg(installation_id), sqlc.arg(payload_path), sqlc.arg(store_root) FROM owned
  ON CONFLICT (team_id, bot_id, dependency_id) DO UPDATE
  SET desired_revision = EXCLUDED.desired_revision, version = EXCLUDED.version, source_url = EXCLUDED.source_url, registry_id = EXCLUDED.registry_id,
      definition_revision = EXCLUDED.definition_revision, manifest_digest = EXCLUDED.manifest_digest,
      authorized_at = now(), authorized_by_operation_id = EXCLUDED.authorized_by_operation_id, authorized_by_actor = EXCLUDED.authorized_by_actor,
      platform_os = EXCLUDED.platform_os, platform_arch = EXCLUDED.platform_arch, platform_libc = EXCLUDED.platform_libc,
      entrypoints = EXCLUDED.entrypoints, installation_id = EXCLUDED.installation_id, payload_path = EXCLUDED.payload_path, store_root = EXCLUDED.store_root,
      repair_status = 'ready', repair_operation_id = '', repair_attempts = 0, repair_next_attempt_at = NULL, repair_last_error_code = '', updated_at = now()
  RETURNING *
), audited AS (
  INSERT INTO bot_dependency_authorization_events (bot_id, dependency_id, operation_id, action, actor, version, source_url, registry_id, definition_revision, manifest_digest)
  SELECT bot_id, dependency_id, authorized_by_operation_id, 'authorize', authorized_by_actor, version, source_url, registry_id, definition_revision, manifest_digest FROM desired
  ON CONFLICT DO NOTHING
)
SELECT * FROM owned;

-- name: FinishDesiredDependencyRepair :one
-- Keep the desired -> installation lock order shared by all managed writes.
WITH target_lock AS MATERIALIZED (
  SELECT d.desired_revision FROM bot_dependency_desired_installations d
  WHERE d.team_id = public.memoh_current_team_id() AND d.bot_id = sqlc.arg(bot_id) AND d.dependency_id = sqlc.arg(dependency_id)
  FOR UPDATE
), owned AS (
  SELECT i.* FROM bot_dependency_installations i
  WHERE i.team_id = public.memoh_current_team_id() AND i.bot_id = sqlc.arg(bot_id) AND i.dependency_id = sqlc.arg(dependency_id)
    AND i.operation_id = sqlc.arg(operation_id) AND operation_id <> ''
    AND (SELECT count(*) FROM target_lock) >= 0 FOR UPDATE
), repaired AS (
  UPDATE bot_dependency_desired_installations d
  SET entrypoints = sqlc.arg(entrypoints)::jsonb, installation_id = sqlc.arg(installation_id), payload_path = sqlc.arg(payload_path), store_root = sqlc.arg(store_root),
      repair_status = 'ready', repair_operation_id = '', repair_next_attempt_at = NULL, repair_last_error_code = '', updated_at = now()
  WHERE d.team_id = public.memoh_current_team_id() AND d.bot_id = sqlc.arg(bot_id) AND d.dependency_id = sqlc.arg(dependency_id)
    AND d.desired_revision = sqlc.arg(desired_revision) AND d.repair_operation_id = sqlc.arg(operation_id)
    AND d.version = sqlc.arg(version) AND EXISTS (SELECT 1 FROM owned) RETURNING *
)
UPDATE bot_dependency_installations i
SET source = 'managed', status = 'installed', installed_version = d.version, manifest_digest = d.manifest_digest,
    source_url = d.source_url, registry_id = d.registry_id, definition_revision = d.definition_revision,
    last_operation_id = i.operation_id, operation_id = '', operation_source_url = '', operation_registry_id = '', operation_definition_revision = '', operation_intent = NULL, last_error = '', updated_at = now()
FROM repaired d
WHERE i.team_id = d.team_id AND i.bot_id = d.bot_id AND i.dependency_id = d.dependency_id AND i.operation_id = sqlc.arg(operation_id)
RETURNING i.*;
