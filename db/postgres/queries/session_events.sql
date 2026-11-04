-- name: NextSessionEventCursor :one
SELECT nextval('bot_session_event_cursor_seq')::bigint;

-- name: CreateSessionEvent :one
INSERT INTO bot_session_events (
  bot_id,
  session_id,
  event_kind,
  event_data,
  external_message_id,
  sender_channel_identity_id,
  received_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT DO NOTHING
RETURNING id;

-- name: ListSessionEventsBySession :many
SELECT * FROM bot_session_events
WHERE team_id = public.memoh_current_team_id() AND session_id = $1
ORDER BY received_at_ms ASC, created_at ASC, id ASC;

-- name: ListSessionEventsBySessionAfter :many
SELECT * FROM bot_session_events
WHERE team_id = public.memoh_current_team_id() AND session_id = $1 AND received_at_ms >= $2
ORDER BY received_at_ms ASC, created_at ASC, id ASC;

-- name: ListSessionEventsBySessionPageBeforeWithinBytes :many
-- Replay reads newest-first with a stable keyset and a hard page-byte budget.
-- What the session's checkpoint stands for is excluded in SQL before
-- event_data reaches the process: every event of a message whose context
-- rows all lie at or before the checkpoint's position without being kept by
-- reference. Its summary stands for the message as its rows recorded it, so
-- an edit after the fold stays out too. Each event is decided against the
-- rows of its own message, so a page costs the events it reads, not the
-- session's history.
WITH page AS (
  SELECT
    event.id,
    event.team_id,
    event.bot_id,
    event.session_id,
    event.event_kind,
    event.event_data,
    event.external_message_id,
    event.sender_channel_identity_id,
    event.received_at_ms,
    event.created_at,
    octet_length(event.event_data::text)::BIGINT AS payload_bytes
  FROM bot_session_events event
  WHERE event.team_id = public.memoh_current_team_id()
    AND event.session_id = sqlc.arg(session_id)
    AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (event.received_at_ms, event.created_at, event.id) < (
        sqlc.arg(before_received_at_ms)::BIGINT,
        sqlc.arg(before_created_at)::TIMESTAMPTZ,
        sqlc.arg(before_id)::UUID
      )
    )
    AND (
      SELECT bool_or(
        (m.turn_position, m.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
        OR m.id = ANY(sqlc.arg(kept_ids)::uuid[])
      )
      FROM bot_visible_history_messages m
      WHERE m.team_id = public.memoh_current_team_id()
        AND m.session_id = sqlc.arg(session_id)
        AND m.source_message_id = event.external_message_id
        AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
    ) IS NOT FALSE
  ORDER BY event.received_at_ms DESC, event.created_at DESC, event.id DESC
  LIMIT sqlc.arg(page_size)
), admitted AS (
  SELECT
    page.*,
    SUM(page.payload_bytes) OVER (
      ORDER BY page.received_at_ms DESC, page.created_at DESC, page.id DESC
    )::BIGINT AS cumulative_bytes
  FROM page
)
SELECT
  admitted.id,
  admitted.team_id,
  admitted.bot_id,
  admitted.session_id,
  admitted.event_kind,
  admitted.event_data,
  admitted.external_message_id,
  admitted.sender_channel_identity_id,
  admitted.received_at_ms,
  admitted.created_at,
  admitted.payload_bytes,
  admitted.cumulative_bytes
FROM admitted
WHERE admitted.cumulative_bytes <= sqlc.arg(max_bytes)::BIGINT
ORDER BY admitted.received_at_ms DESC, admitted.created_at DESC, admitted.id DESC;

-- name: ListSessionEventsByBot :many
SELECT * FROM bot_session_events
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1
ORDER BY received_at_ms ASC, id ASC;

-- name: CountSessionEvents :one
SELECT COUNT(*) FROM bot_session_events
WHERE team_id = public.memoh_current_team_id() AND session_id = $1;

-- name: DeleteSessionEventsByBot :exec
DELETE FROM bot_session_events
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1;
