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
-- reference. A folded message that changed after its rows were written
-- replays whole: its summary read the rows, never the change. The change is
-- the events after the one its rows were written from or, when that cursor
-- is unknown, an edit.
WITH message_rows AS MATERIALIZED (
  SELECT
    m.source_message_id AS external_message_id,
    bool_or(
      (m.turn_position, m.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
      OR m.id = ANY(sqlc.arg(kept_ids)::uuid[])
    ) AS kept,
    COALESCE(MAX((row_event.event_data->>'event_cursor')::bigint), 0)::bigint AS event_cursor
  FROM bot_visible_history_messages m
  LEFT JOIN bot_session_events row_event
    ON row_event.team_id = public.memoh_current_team_id()
   AND row_event.id = m.event_id
  WHERE m.team_id = public.memoh_current_team_id()
    AND m.session_id = sqlc.arg(session_id)
    AND m.source_message_id IS NOT NULL
    AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
  GROUP BY m.source_message_id
), changed_messages AS MATERIALIZED (
  SELECT message.external_message_id
  FROM message_rows message
  JOIN bot_session_events event
    ON event.team_id = public.memoh_current_team_id()
   AND event.session_id = sqlc.arg(session_id)
   AND event.external_message_id = message.external_message_id
  WHERE NOT message.kept
  GROUP BY message.external_message_id, message.event_cursor
  HAVING CASE
      WHEN message.event_cursor > 0
        THEN MAX(COALESCE((event.event_data->>'event_cursor')::bigint, 0)) > message.event_cursor
      ELSE bool_or(event.event_kind = 'edit')
    END
), candidates AS MATERIALIZED (
  SELECT
    event.id,
    event.received_at_ms,
    event.created_at,
    octet_length(event.event_data::text)::BIGINT AS payload_bytes
  FROM bot_session_events event
  LEFT JOIN message_rows message
    ON message.external_message_id = event.external_message_id
  LEFT JOIN changed_messages changed
    ON changed.external_message_id = event.external_message_id
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
      message.kept
      OR changed.external_message_id IS NOT NULL
      OR message.external_message_id IS NULL
    )
), ranked AS MATERIALIZED (
  SELECT
    candidates.*,
    SUM(payload_bytes) OVER (
      ORDER BY received_at_ms DESC, created_at DESC, id DESC
    )::BIGINT AS cumulative_bytes
  FROM candidates
), admitted AS MATERIALIZED (
  SELECT *
  FROM ranked
  WHERE cumulative_bytes <= sqlc.arg(max_bytes)::BIGINT
  ORDER BY received_at_ms DESC, created_at DESC, id DESC
  LIMIT sqlc.arg(page_size)
)
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
  admitted.payload_bytes,
  admitted.cumulative_bytes
FROM admitted
JOIN bot_session_events event ON event.id = admitted.id
ORDER BY event.received_at_ms DESC, event.created_at DESC, event.id DESC;

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
