-- 0103_message_turn_read_model
-- Add single-table history turn read/lifecycle model and session turn position allocator.

ALTER TABLE bot_history_messages
  ADD COLUMN IF NOT EXISTS turn_id UUID,
  ADD COLUMN IF NOT EXISTS turn_message_seq BIGINT,
  ADD COLUMN IF NOT EXISTS turn_position BIGINT,
  ADD COLUMN IF NOT EXISTS turn_visible BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS turn_superseded_by_turn_id UUID,
  ADD COLUMN IF NOT EXISTS turn_superseded_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS turn_superseded_reason TEXT;

CREATE INDEX IF NOT EXISTS idx_bot_history_messages_session_role_created
  ON bot_history_messages(session_id, role, created_at, id);
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_turn
  ON bot_history_messages(turn_id, turn_message_seq, created_at, id)
  WHERE turn_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_history_messages_turn_seq_unique
  ON bot_history_messages(turn_id, turn_message_seq)
  WHERE turn_id IS NOT NULL AND turn_message_seq IS NOT NULL;

DROP TABLE IF EXISTS _memoh_history_turn_backfill;
CREATE TEMP TABLE _memoh_history_turn_backfill AS
WITH ordered AS (
  SELECT
    m.*,
    COUNT(*) FILTER (WHERE m.role = 'user') OVER (
      PARTITION BY m.session_id
      ORDER BY m.created_at, m.id
      ROWS UNBOUNDED PRECEDING
    ) AS user_group
  FROM bot_history_messages m
  WHERE m.session_id IS NOT NULL
    AND m.role IN ('user', 'assistant')
),
grouped AS (
  SELECT
    CASE
      WHEN role = 'user' THEN user_group
      WHEN user_group = 0 THEN -ROW_NUMBER() OVER (
        PARTITION BY session_id
        ORDER BY created_at, id
      )
      ELSE user_group
    END AS turn_group,
    *
  FROM ordered
),
turns AS (
  SELECT
    gen_random_uuid() AS turn_id,
    bot_id,
    session_id,
    turn_group,
    MIN(created_at) AS created_at,
    (ARRAY_AGG(id ORDER BY created_at, id) FILTER (WHERE role = 'user'))[1] AS request_message_id,
    (ARRAY_AGG(id ORDER BY created_at, id) FILTER (WHERE role = 'assistant'))[1] AS assistant_message_id
  FROM grouped
  GROUP BY bot_id, session_id, turn_group
)
SELECT
  *,
  ROW_NUMBER() OVER (
    PARTITION BY session_id
    ORDER BY created_at, turn_group
  ) AS position
FROM turns;

UPDATE bot_history_messages m
SET turn_id = t.turn_id,
    turn_message_seq = 1,
    turn_position = t.position,
    turn_visible = true
FROM _memoh_history_turn_backfill t
WHERE m.id = t.request_message_id
  AND m.turn_id IS NULL;

UPDATE bot_history_messages m
SET turn_id = t.turn_id,
    turn_message_seq = 2,
    turn_position = t.position,
    turn_visible = true
FROM _memoh_history_turn_backfill t
WHERE m.id = t.assistant_message_id
  AND m.turn_id IS NULL;

WITH bounded_turns AS (
  SELECT
    t.turn_id,
    t.session_id,
    t.position,
    assistant.created_at AS assistant_created_at,
    assistant.id AS assistant_id,
    LEAD(COALESCE(req.created_at, assistant.created_at)) OVER (
      PARTITION BY t.session_id
      ORDER BY t.position
    ) AS next_created_at,
    LEAD(COALESCE(req.id, assistant.id)) OVER (
      PARTITION BY t.session_id
      ORDER BY t.position
    ) AS next_message_id
  FROM _memoh_history_turn_backfill t
  LEFT JOIN bot_history_messages req ON req.id = t.request_message_id
  LEFT JOIN bot_history_messages assistant ON assistant.id = t.assistant_message_id
),
tail_messages AS (
  SELECT
    m.id AS message_id,
    t.turn_id,
    t.position,
    2 + ROW_NUMBER() OVER (
      PARTITION BY t.turn_id
      ORDER BY m.created_at, m.id
    ) AS turn_message_seq
  FROM bounded_turns t
  JOIN bot_history_messages m
    ON m.session_id = t.session_id
   AND m.role IN ('assistant', 'tool')
  WHERE t.assistant_id IS NOT NULL
    AND m.id <> t.assistant_id
    AND m.turn_id IS NULL
    AND NOT EXISTS (
      SELECT 1
      FROM _memoh_history_turn_backfill anchored
      WHERE anchored.request_message_id = m.id
         OR anchored.assistant_message_id = m.id
    )
    AND (m.created_at, m.id) > (t.assistant_created_at, t.assistant_id)
    AND (
      t.next_created_at IS NULL
      OR (m.created_at, m.id) < (t.next_created_at, t.next_message_id)
    )
)
UPDATE bot_history_messages m
SET turn_id = tail.turn_id,
    turn_message_seq = tail.turn_message_seq,
    turn_position = tail.position,
    turn_visible = true
FROM tail_messages tail
WHERE m.id = tail.message_id;

CREATE INDEX IF NOT EXISTS idx_bot_history_messages_visible_session_order
  ON bot_history_messages(session_id, turn_position DESC, turn_message_seq DESC, created_at DESC, id DESC)
  WHERE turn_visible = true
    AND turn_id IS NOT NULL
    AND turn_position IS NOT NULL
    AND turn_message_seq IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_bot_history_messages_visible_session_source_order
  ON bot_history_messages(session_id, source_message_id, turn_position DESC, turn_message_seq DESC, created_at DESC, id DESC)
  WHERE turn_visible = true
    AND source_message_id IS NOT NULL
    AND turn_id IS NOT NULL
    AND turn_position IS NOT NULL
    AND turn_message_seq IS NOT NULL;

-- The canonical 0001 already exposes the final team_id projection. Replaying
-- this historical shape must drop the view because CREATE OR REPLACE cannot
-- remove or reorder existing columns; 0112 restores the team-safe shape.
DROP VIEW IF EXISTS bot_visible_history_messages;
CREATE VIEW bot_visible_history_messages AS
SELECT
  m.turn_id,
  m.turn_position,
  m.turn_message_seq,
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id,
  m.source_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.compact_id,
  m.session_mode,
  m.runtime_type,
  m.model_id,
  m.event_id,
  m.display_text,
  m.created_at
FROM bot_history_messages m
WHERE m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL;

ALTER TABLE bot_sessions
  ADD COLUMN IF NOT EXISTS next_turn_position BIGINT NOT NULL DEFAULT 1;

UPDATE bot_sessions s
SET next_turn_position = GREATEST(s.next_turn_position, turns.next_position)
FROM (
  SELECT session_id, MAX(turn_position) + 1 AS next_position
  FROM bot_history_messages
  WHERE turn_position IS NOT NULL
  GROUP BY session_id
) turns
WHERE s.id = turns.session_id;

DROP TABLE IF EXISTS _memoh_history_turn_backfill;
