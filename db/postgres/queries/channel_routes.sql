-- name: CreateChatRoute :one
INSERT INTO bot_channel_routes (
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
FROM bot_channel_routes
WHERE team_id = public.memoh_current_team_id()
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
FROM bot_channel_routes
WHERE team_id = public.memoh_current_team_id() AND id = $1;

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
FROM bot_channel_routes
WHERE team_id = public.memoh_current_team_id() AND bot_id = sqlc.arg(bot_id)
ORDER BY created_at ASC;

-- name: ListChatRouteThreadProjectionsByIDs :many
SELECT
  id,
  conversation_type,
  metadata
FROM bot_channel_routes
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = ANY(sqlc.arg(route_ids)::uuid[]);

-- name: UpdateChatRouteReplyTarget :exec
UPDATE bot_channel_routes
SET default_reply_target = sqlc.arg(reply_target), updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: UpdateChatRouteMetadata :exec
UPDATE bot_channel_routes
SET metadata = sqlc.arg(metadata), updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: SetRouteActiveSession :exec
WITH destination_session AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = sqlc.narg(active_session_id)::uuid
  FOR KEY SHARE
)
UPDATE bot_channel_routes route
SET active_session_id = COALESCE(
      (SELECT destination_session.id FROM destination_session),
      sqlc.narg(active_session_id)::uuid
    ),
    updated_at = now()
WHERE route.team_id = public.memoh_current_team_id()
  AND route.id = sqlc.arg(id);

-- name: CompareAndSetRouteActiveSession :execrows
-- Moves the pointer only while it still holds expected_session_id (NULL for a
-- route that has none), so concurrent creators of a route's first session agree
-- on one: the update that finds the pointer changed affects no row.
WITH destination_session AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = sqlc.narg(active_session_id)::uuid
  FOR KEY SHARE
)
UPDATE bot_channel_routes route
SET active_session_id = COALESCE(
      (SELECT destination_session.id FROM destination_session),
      sqlc.narg(active_session_id)::uuid
    ),
    updated_at = now()
WHERE route.team_id = public.memoh_current_team_id()
  AND route.id = sqlc.arg(id)
  AND route.active_session_id IS NOT DISTINCT FROM sqlc.narg(expected_session_id)::uuid;

-- name: DeleteChatRoute :exec
WITH route_sessions AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.route_id = sqlc.arg(id)
  ORDER BY session.id
  FOR UPDATE
)
DELETE FROM bot_channel_routes route
WHERE route.team_id = public.memoh_current_team_id()
  AND route.id = sqlc.arg(id)
  AND (SELECT count(*) FROM route_sessions) >= 0;
