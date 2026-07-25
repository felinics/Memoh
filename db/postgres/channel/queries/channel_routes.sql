-- name: CreateChatRoute :one
INSERT INTO channel.bot_channel_routes (
  bot_id, channel_type, channel_config_id, external_conversation_id, external_thread_id, conversation_type, default_reply_target, metadata
)
VALUES (
  sqlc.arg(bot_id),
  sqlc.arg(platform),
  sqlc.narg(channel_config_id)::uuid,
  sqlc.arg(conversation_id),
  sqlc.narg(thread_id)::text,
  sqlc.narg(conversation_type)::text,
  sqlc.narg(reply_target)::text,
  sqlc.arg(metadata)
)
RETURNING
  id,
  bot_id,
  channel_type AS platform,
  channel_config_id,
  external_conversation_id AS conversation_id,
  external_thread_id AS thread_id,
  conversation_type,
  default_reply_target AS reply_target,
  active_session_id,
  metadata,
  created_at,
  updated_at;

-- name: FindChatRoute :one
SELECT
  id,
  bot_id,
  channel_type AS platform,
  channel_config_id,
  external_conversation_id AS conversation_id,
  external_thread_id AS thread_id,
  conversation_type,
  default_reply_target AS reply_target,
  active_session_id,
  metadata,
  created_at,
  updated_at
FROM channel.bot_channel_routes
WHERE team_id = iam.memoh_current_team_id()
  AND bot_id = $1
  AND channel_type = sqlc.arg(platform)
  AND external_conversation_id = sqlc.arg(conversation_id)
  AND COALESCE(external_thread_id, '') = COALESCE(sqlc.narg(thread_id), '')
LIMIT 1;

-- name: GetChatRouteByID :one
SELECT
  id,
  bot_id,
  channel_type AS platform,
  channel_config_id,
  external_conversation_id AS conversation_id,
  external_thread_id AS thread_id,
  conversation_type,
  default_reply_target AS reply_target,
  active_session_id,
  metadata,
  created_at,
  updated_at
FROM channel.bot_channel_routes
WHERE team_id = iam.memoh_current_team_id() AND id = $1;

-- name: ListChatRoutes :many
SELECT
  id,
  bot_id,
  channel_type AS platform,
  channel_config_id,
  external_conversation_id AS conversation_id,
  external_thread_id AS thread_id,
  conversation_type,
  default_reply_target AS reply_target,
  active_session_id,
  metadata,
  created_at,
  updated_at
FROM channel.bot_channel_routes
WHERE team_id = iam.memoh_current_team_id() AND bot_id = sqlc.arg(bot_id)
ORDER BY created_at ASC;

-- name: UpdateChatRouteReplyTarget :exec
UPDATE channel.bot_channel_routes
SET default_reply_target = sqlc.arg(reply_target), updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: UpdateChatRouteMetadata :exec
UPDATE channel.bot_channel_routes
SET metadata = sqlc.arg(metadata), updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: SetRouteActiveSession :exec
UPDATE channel.bot_channel_routes
SET active_session_id = sqlc.narg(active_session_id)::uuid,
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id()
  AND id = sqlc.arg(id);

-- name: DeleteChatRoute :exec
DELETE FROM channel.bot_channel_routes
WHERE team_id = iam.memoh_current_team_id()
  AND id = sqlc.arg(id);
