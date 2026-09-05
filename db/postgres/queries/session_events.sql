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
-- Covered message/edit payloads are excluded in SQL before event_data reaches
-- the process. If a covered message has a post-frontier mutation, its complete
-- event chain remains eligible so edit projection still has its base node.
WITH post_coverage_ids AS MATERIALIZED (
  SELECT DISTINCT changed.external_message_id
  FROM bot_session_events changed
  WHERE changed.team_id = public.memoh_current_team_id()
    AND changed.session_id = sqlc.arg(session_id)
    AND changed.external_message_id IS NOT NULL
    AND sqlc.arg(covered_external_messages)::jsonb ? changed.external_message_id
    AND changed.received_at_ms > COALESCE(
      (sqlc.arg(covered_external_messages)::jsonb ->> changed.external_message_id)::BIGINT,
      0
    )
), candidates AS MATERIALIZED (
  SELECT
    event.id,
    event.received_at_ms,
    event.created_at,
    octet_length(event.event_data::text)::BIGINT AS payload_bytes
  FROM bot_session_events event
  LEFT JOIN post_coverage_ids changed
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
      event.external_message_id IS NULL
      OR NOT (sqlc.arg(covered_external_messages)::jsonb ? event.external_message_id)
      OR event.received_at_ms > COALESCE(
        (sqlc.arg(covered_external_messages)::jsonb ->> event.external_message_id)::BIGINT,
        0
      )
      OR changed.external_message_id IS NOT NULL
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
