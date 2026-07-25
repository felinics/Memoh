-- name: GetSessionDiscussCursor :one
SELECT *
FROM channel.bot_session_discuss_cursors
WHERE team_id = iam.memoh_current_team_id()
  AND session_id = sqlc.arg(session_id)
  AND scope_key = sqlc.arg(scope_key);

-- name: UpsertSessionDiscussCursor :one
INSERT INTO channel.bot_session_discuss_cursors (
  session_id, bot_id, scope_key, route_id, source, consumed_cursor, consumed_event_cursor
)
VALUES (
  sqlc.arg(session_id),
  sqlc.arg(bot_id),
  sqlc.arg(scope_key),
  sqlc.narg(route_id)::uuid,
  sqlc.arg(source),
  sqlc.arg(consumed_cursor),
  sqlc.arg(consumed_event_cursor)
)
ON CONFLICT (team_id, session_id, scope_key) DO UPDATE
SET route_id = COALESCE(EXCLUDED.route_id, channel.bot_session_discuss_cursors.route_id),
    source = EXCLUDED.source,
    consumed_cursor = GREATEST(channel.bot_session_discuss_cursors.consumed_cursor, EXCLUDED.consumed_cursor),
    consumed_event_cursor = GREATEST(channel.bot_session_discuss_cursors.consumed_event_cursor, EXCLUDED.consumed_event_cursor),
    updated_at = now()
RETURNING *;

-- name: ListSessionDiscussCursorsByBot :many
SELECT *
FROM channel.bot_session_discuss_cursors
WHERE team_id = iam.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
ORDER BY updated_at ASC, session_id ASC, scope_key ASC;

-- name: DeleteSessionDiscussCursorsByBot :exec
DELETE FROM channel.bot_session_discuss_cursors
WHERE team_id = iam.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id);
