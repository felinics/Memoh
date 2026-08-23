-- name: CreateUserRuntime :one
INSERT INTO user_runtimes (user_id, name, api_token)
VALUES (sqlc.arg(user_id), sqlc.arg(name), sqlc.arg(api_token))
RETURNING *;

-- name: GetUserRuntimeByAPIToken :one
SELECT runtime.*
FROM user_runtimes runtime
JOIN team_members owner_membership
  ON owner_membership.team_id = public.memoh_current_team_id()
 AND owner_membership.user_id = runtime.user_id
 AND owner_membership.is_active = TRUE
JOIN users owner ON owner.id = owner_membership.user_id AND owner.is_active = TRUE
WHERE runtime.team_id = public.memoh_current_team_id()
  AND runtime.api_token = sqlc.arg(api_token)
  AND runtime.revoked_at IS NULL
  AND (runtime.activated_at IS NOT NULL OR runtime.pending_expires_at > now());

-- name: ActivateUserRuntime :one
-- The first ready connection consumes the short-lived pending state. Existing
-- activated credentials remain reusable for later reconnects.
UPDATE user_runtimes runtime
SET activated_at = COALESCE(runtime.activated_at, now()),
    pending_expires_at = NULL,
    updated_at = CASE WHEN runtime.activated_at IS NULL THEN now() ELSE runtime.updated_at END
FROM team_members owner_membership, users owner
WHERE runtime.team_id = public.memoh_current_team_id()
  AND runtime.id = sqlc.arg(id)
  AND runtime.api_token = sqlc.arg(api_token)
  AND runtime.revoked_at IS NULL
  AND (runtime.activated_at IS NOT NULL OR runtime.pending_expires_at > now())
  AND owner_membership.team_id = public.memoh_current_team_id()
  AND owner_membership.user_id = runtime.user_id
  AND owner_membership.is_active = TRUE
  AND owner.id = owner_membership.user_id
  AND owner.is_active = TRUE
RETURNING runtime.*;

-- name: ExpirePendingUserRuntimes :exec
-- Expired attempts can no longer authenticate. Mark them revoked and remove
-- any defensive direct-API mounts so they release names and grant rows too.
WITH expired AS (
  UPDATE user_runtimes
  SET revoked_at = now(), updated_at = now()
  WHERE team_id = public.memoh_current_team_id()
    AND user_id = sqlc.arg(user_id)
    AND revoked_at IS NULL
    AND activated_at IS NULL
    AND pending_expires_at <= now()
  RETURNING id
)
DELETE FROM bot_remote_runtime_bindings binding
USING expired
WHERE binding.team_id = public.memoh_current_team_id()
  AND binding.runtime_id = expired.id;

-- name: ListUserRuntimes :many
SELECT * FROM user_runtimes
WHERE team_id = public.memoh_current_team_id()
  AND user_id = sqlc.arg(user_id) AND revoked_at IS NULL
ORDER BY created_at ASC, id ASC;

-- name: RevokeUserRuntime :one
UPDATE user_runtimes
SET revoked_at = now(), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND revoked_at IS NULL
RETURNING *;

-- name: DeleteBotRemoteRuntimeMountsByRuntime :exec
-- Revoking a runtime kills every bot mount of it in the same transaction:
-- dead bindings would otherwise linger as ghost rows on every surface.
WITH deleted_packages AS (
  DELETE FROM bot_skill_package_installations AS package
  USING bot_remote_runtime_bindings AS binding
  WHERE package.team_id = public.memoh_current_team_id()
    AND package.bot_id = binding.bot_id
    AND package.workspace_target_id = binding.id::text
    AND binding.team_id = public.memoh_current_team_id()
    AND binding.runtime_id = sqlc.arg(runtime_id)
)
DELETE FROM bot_remote_runtime_bindings AS binding
WHERE binding.team_id = public.memoh_current_team_id()
  AND binding.runtime_id = sqlc.arg(runtime_id);

-- name: BackfillUserRuntimeName :execrows
-- Fills the display name from the connecting machine, but ONLY while the row
-- still carries its creation-time default (or an empty name): a user-chosen
-- name is never overwritten by a later handshake.
UPDATE user_runtimes
SET name = sqlc.arg(name), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL
  AND (name = '' OR name = sqlc.arg(default_name));

-- name: CreateOrUpdateBotRemoteRuntimeMount :one
INSERT INTO bot_remote_runtime_bindings (bot_id, runtime_id)
SELECT b.id, r.id
FROM bots b
JOIN user_runtimes r
  ON r.id = sqlc.arg(runtime_id)
 AND r.team_id = public.memoh_current_team_id()
 AND r.user_id = b.owner_user_id
 AND r.revoked_at IS NULL
 AND r.activated_at IS NOT NULL
JOIN team_members owner_membership
  ON owner_membership.team_id = public.memoh_current_team_id()
 AND owner_membership.user_id = b.owner_user_id
 AND owner_membership.is_active = TRUE
JOIN users owner ON owner.id = owner_membership.user_id AND owner.is_active = TRUE
WHERE b.team_id = public.memoh_current_team_id()
  AND b.id = sqlc.arg(bot_id)
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
  (runtime.revoked_at IS NOT NULL OR NOT owner.is_active OR NOT owner_membership.is_active) AS runtime_unavailable,
  bot.owner_user_id AS bot_owner_user_id
FROM bot_remote_runtime_bindings binding
JOIN user_runtimes runtime ON runtime.id = binding.runtime_id AND runtime.team_id = public.memoh_current_team_id()
JOIN bots bot ON bot.id = binding.bot_id AND bot.team_id = public.memoh_current_team_id()
JOIN team_members owner_membership
  ON owner_membership.team_id = public.memoh_current_team_id()
 AND owner_membership.user_id = bot.owner_user_id
JOIN users owner ON owner.id = owner_membership.user_id
WHERE binding.team_id = public.memoh_current_team_id()
  AND binding.bot_id = sqlc.arg(bot_id)
  AND runtime.revoked_at IS NULL
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
  (runtime.revoked_at IS NOT NULL OR NOT owner.is_active OR NOT owner_membership.is_active) AS runtime_unavailable,
  bot.owner_user_id AS bot_owner_user_id
FROM bot_remote_runtime_bindings binding
JOIN user_runtimes runtime ON runtime.id = binding.runtime_id AND runtime.team_id = public.memoh_current_team_id()
JOIN bots bot ON bot.id = binding.bot_id AND bot.team_id = public.memoh_current_team_id()
JOIN team_members owner_membership
  ON owner_membership.team_id = public.memoh_current_team_id()
 AND owner_membership.user_id = bot.owner_user_id
JOIN users owner ON owner.id = owner_membership.user_id
WHERE binding.team_id = public.memoh_current_team_id()
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
  (runtime.revoked_at IS NOT NULL OR NOT owner.is_active OR NOT owner_membership.is_active) AS runtime_unavailable,
  bot.owner_user_id AS bot_owner_user_id
FROM bot_remote_runtime_bindings binding
JOIN user_runtimes runtime ON runtime.id = binding.runtime_id AND runtime.team_id = public.memoh_current_team_id()
JOIN bots bot ON bot.id = binding.bot_id AND bot.team_id = public.memoh_current_team_id()
JOIN team_members owner_membership
  ON owner_membership.team_id = public.memoh_current_team_id()
 AND owner_membership.user_id = bot.owner_user_id
JOIN users owner ON owner.id = owner_membership.user_id
WHERE binding.team_id = public.memoh_current_team_id()
  AND binding.bot_id = sqlc.arg(bot_id)
  AND binding.is_primary = TRUE;

-- name: ClearBotRemoteRuntimePrimary :exec
UPDATE bot_remote_runtime_bindings
SET is_primary = FALSE,
    updated_at = CASE WHEN is_primary THEN now() ELSE updated_at END
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id);

-- name: SetBotRemoteRuntimePrimary :execrows
UPDATE bot_remote_runtime_bindings
SET is_primary = TRUE,
    updated_at = CASE WHEN is_primary THEN updated_at ELSE now() END
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(target_id);

-- name: UpdateBotRemoteRuntimeMountToolApproval :one
UPDATE bot_remote_runtime_bindings
SET tool_approval_config = sqlc.arg(tool_approval_config),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(target_id)
RETURNING id;

-- name: DeleteBotRemoteRuntimeMount :one
WITH deleted_packages AS (
  DELETE FROM bot_skill_package_installations AS package
  USING bot_remote_runtime_bindings AS binding
  WHERE package.team_id = public.memoh_current_team_id()
    AND package.bot_id = binding.bot_id
    AND package.workspace_target_id = binding.id::text
    AND binding.team_id = public.memoh_current_team_id()
    AND binding.bot_id = sqlc.arg(bot_id)
    AND binding.id = sqlc.arg(target_id)
)
DELETE FROM bot_remote_runtime_bindings AS binding
WHERE binding.team_id = public.memoh_current_team_id()
  AND binding.bot_id = sqlc.arg(bot_id)
  AND binding.id = sqlc.arg(target_id)
RETURNING id;

-- name: ListBotRemoteRuntimeGrantsByRuntimeOwner :many
-- Account-level reverse lookup: every live mount held by the owner's bots.
-- A mount can only exist while runtime.user_id = bot.owner_user_id (enforced
-- by CreateOrUpdateBotRemoteRuntimeMount), so scoping by the bot owner covers
-- exactly the grants on the owner's own runtimes.
SELECT
  binding.id,
  binding.bot_id,
  binding.runtime_id,
  binding.is_primary
FROM bot_remote_runtime_bindings binding
JOIN bots bot ON bot.id = binding.bot_id AND bot.team_id = public.memoh_current_team_id()
JOIN user_runtimes runtime ON runtime.id = binding.runtime_id AND runtime.team_id = public.memoh_current_team_id()
WHERE binding.team_id = public.memoh_current_team_id()
  AND bot.owner_user_id = sqlc.arg(owner_user_id)
  AND runtime.revoked_at IS NULL
ORDER BY binding.created_at ASC, binding.id ASC;
