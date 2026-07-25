-- name: CreateOrUpdateBotRemoteRuntimeMount :one
INSERT INTO runtime.bot_remote_runtime_bindings (bot_id, runtime_id)
SELECT sqlc.arg(bot_id), runtime.id
FROM runtime.user_runtimes runtime
WHERE runtime.team_id = iam.memoh_current_team_id()
  AND runtime.id = sqlc.arg(runtime_id)
  AND runtime.user_id = sqlc.arg(owner_user_id)
  AND runtime.revoked_at IS NULL
ON CONFLICT (team_id, bot_id, runtime_id) DO UPDATE SET
  updated_at = now()
RETURNING id;

-- name: ListBotRemoteRuntimeMounts :many
SELECT
  binding.id,
  binding.bot_id,
  binding.runtime_id,
  binding.is_primary,
  binding.tool_approval_config,
  binding.created_at,
  binding.updated_at,
  runtime.name AS runtime_name,
  runtime.user_id AS runtime_user_id,
  runtime.revoked_at
FROM runtime.bot_remote_runtime_bindings binding
JOIN runtime.user_runtimes runtime
  ON runtime.team_id = binding.team_id
 AND runtime.id = binding.runtime_id
WHERE binding.team_id = iam.memoh_current_team_id()
  AND binding.bot_id = sqlc.arg(bot_id)
ORDER BY binding.created_at ASC, binding.id ASC;

-- name: GetBotRemoteRuntimeMount :one
SELECT
  binding.id,
  binding.bot_id,
  binding.runtime_id,
  binding.is_primary,
  binding.tool_approval_config,
  binding.created_at,
  binding.updated_at,
  runtime.name AS runtime_name,
  runtime.user_id AS runtime_user_id,
  runtime.revoked_at
FROM runtime.bot_remote_runtime_bindings binding
JOIN runtime.user_runtimes runtime
  ON runtime.team_id = binding.team_id
 AND runtime.id = binding.runtime_id
WHERE binding.team_id = iam.memoh_current_team_id()
  AND binding.bot_id = sqlc.arg(bot_id)
  AND binding.id = sqlc.arg(target_id);

-- name: GetPrimaryBotRemoteRuntimeMount :one
SELECT
  binding.id,
  binding.bot_id,
  binding.runtime_id,
  binding.is_primary,
  binding.tool_approval_config,
  binding.created_at,
  binding.updated_at,
  runtime.name AS runtime_name,
  runtime.user_id AS runtime_user_id,
  runtime.revoked_at
FROM runtime.bot_remote_runtime_bindings binding
JOIN runtime.user_runtimes runtime
  ON runtime.team_id = binding.team_id
 AND runtime.id = binding.runtime_id
WHERE binding.team_id = iam.memoh_current_team_id()
  AND binding.bot_id = sqlc.arg(bot_id)
  AND binding.is_primary = TRUE;

-- name: ClearBotRemoteRuntimePrimary :exec
UPDATE runtime.bot_remote_runtime_bindings
SET is_primary = FALSE,
    updated_at = CASE WHEN is_primary THEN now() ELSE updated_at END
WHERE team_id = iam.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id);

-- name: SetBotRemoteRuntimePrimary :execrows
UPDATE runtime.bot_remote_runtime_bindings
SET is_primary = TRUE,
    updated_at = CASE WHEN is_primary THEN updated_at ELSE now() END
WHERE team_id = iam.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(target_id);

-- name: UpdateBotRemoteRuntimeMountToolApproval :one
UPDATE runtime.bot_remote_runtime_bindings
SET tool_approval_config = sqlc.arg(tool_approval_config),
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(target_id)
RETURNING id;

-- name: DeleteBotRemoteRuntimeMount :one
DELETE FROM runtime.bot_remote_runtime_bindings
WHERE team_id = iam.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(target_id)
RETURNING id;
