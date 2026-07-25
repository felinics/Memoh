-- 0105_repair_superseded_message_visibility
-- Hide superseded history messages that stayed visible because ReplaceHistoryTurn
-- previously updated superseded metadata and visibility in separate CTE updates.
-- Repair the turn positions and position allocator of incomplete fork sessions
-- that users kept using, and hide the incomplete fork sessions left behind by
-- the old ForkSessionFromAssistantMessage CTE when the request returned an
-- error after copying messages.

UPDATE bot_history_messages
SET turn_visible = false
WHERE turn_visible = true
  AND turn_superseded_at IS NOT NULL;

WITH incomplete_fork_sessions AS (
  SELECT s.id, s.created_at, s.next_turn_position
  FROM bot_sessions s
  WHERE s.deleted_at IS NULL
    AND s.type = 'chat'
    AND s.session_mode = 'chat'
    AND jsonb_typeof(s.metadata->'forked_from') = 'object'
    AND (s.metadata->'forked_from') ? 'session_id'
    AND (s.metadata->'forked_from') ? 'message_id'
    AND NOT ((s.metadata->'forked_from') ? 'fork_message_id')
),
-- A missing fork_message_id alone does not prove broken data: the live retry
-- flow legitimately removes the anchor from healthy forks. Repair only
-- sessions whose turn state is actually damaged: two turns colliding on one
-- position, or the allocator sitting at or below an occupied position.
retained_incomplete_forks AS (
  SELECT f.id, f.created_at
  FROM incomplete_fork_sessions f
  WHERE EXISTS (
    SELECT 1
    FROM bot_history_messages m
    WHERE m.session_id = f.id
      AND m.created_at > f.created_at
  )
  AND (
    EXISTS (
      SELECT 1
      FROM bot_history_messages a
      JOIN bot_history_messages b
        ON b.session_id = a.session_id
       AND b.turn_position = a.turn_position
       AND b.turn_id <> a.turn_id
      WHERE a.session_id = f.id
        AND a.turn_id IS NOT NULL
        AND a.turn_position IS NOT NULL
        AND a.turn_visible = true
        AND b.turn_id IS NOT NULL
        AND b.turn_visible = true
    )
    OR f.next_turn_position <= (
      SELECT MAX(m.turn_position)
      FROM bot_history_messages m
      WHERE m.session_id = f.id
        AND m.turn_position IS NOT NULL
        AND m.turn_visible = true
    )
  )
),
-- Classify turns, not messages: a retry replacement reuses the replaced turn's
-- user message, so a post-fork turn can still contain a message created before
-- the fork session. A turn is copied prefix only when ALL of its messages
-- predate the session; any post-fork message makes the whole turn post-fork.
session_turns AS (
  SELECT
    f.id AS session_id,
    f.created_at AS session_created_at,
    m.turn_id,
    MIN(m.turn_position) AS old_position,
    MIN(m.created_at) AS first_created_at,
    MAX(m.created_at) AS last_created_at
  FROM retained_incomplete_forks f
  JOIN bot_history_messages m
    ON m.session_id = f.id
   AND m.turn_id IS NOT NULL
   AND m.turn_position IS NOT NULL
  GROUP BY f.id, f.created_at, m.turn_id
),
copied_prefix AS (
  SELECT
    f.id AS session_id,
    COALESCE(
      MAX(t.old_position) FILTER (WHERE t.last_created_at <= t.session_created_at),
      0
    )::bigint AS max_position
  FROM retained_incomplete_forks f
  LEFT JOIN session_turns t ON t.session_id = f.id
  GROUP BY f.id
),
post_fork_turns AS (
  SELECT
    t.session_id,
    t.turn_id,
    t.first_created_at,
    t.old_position
  FROM session_turns t
  WHERE t.last_created_at > t.session_created_at
),
renumber_plan AS (
  SELECT
    p.session_id,
    p.turn_id,
    copied_prefix.max_position
      + (ROW_NUMBER() OVER (
          PARTITION BY p.session_id
          ORDER BY p.first_created_at ASC, p.old_position ASC, p.turn_id ASC
        ))::bigint AS turn_position
  FROM post_fork_turns p
  JOIN copied_prefix ON copied_prefix.session_id = p.session_id
),
renumbered_messages AS (
  UPDATE bot_history_messages m
  SET turn_position = renumber_plan.turn_position
  FROM renumber_plan
  WHERE m.session_id = renumber_plan.session_id
    AND m.turn_id = renumber_plan.turn_id
    AND m.turn_position IS DISTINCT FROM renumber_plan.turn_position
  RETURNING m.session_id
),
repaired_next_turn_position AS (
  SELECT
    f.id AS session_id,
    (
      GREATEST(
        COALESCE(copied_prefix.max_position, 0),
        COALESCE(MAX(renumber_plan.turn_position), 0)
      ) + 1
    )::bigint AS value
  FROM retained_incomplete_forks f
  JOIN copied_prefix ON copied_prefix.session_id = f.id
  LEFT JOIN renumber_plan ON renumber_plan.session_id = f.id
  GROUP BY f.id, copied_prefix.max_position
)
-- Touch a session only when the repair actually changed something: either
-- messages were renumbered, or the allocator was behind the occupied positions
-- (covers retained forks whose post-fork messages have no turn yet). Sessions
-- that merely match the filter but are already consistent are left untouched.
UPDATE bot_sessions s
SET next_turn_position = GREATEST(s.next_turn_position, repaired_next_turn_position.value),
    updated_at = now()
FROM repaired_next_turn_position
WHERE s.id = repaired_next_turn_position.session_id
  AND (
    EXISTS (
      SELECT 1
      FROM renumbered_messages
      WHERE renumbered_messages.session_id = s.id
    )
    OR s.next_turn_position < repaired_next_turn_position.value
  );

WITH incomplete_forks AS (
  SELECT s.id
  FROM bot_sessions s
  WHERE s.deleted_at IS NULL
    AND s.type = 'chat'
    AND s.session_mode = 'chat'
    AND jsonb_typeof(s.metadata->'forked_from') = 'object'
    AND (s.metadata->'forked_from') ? 'session_id'
    AND (s.metadata->'forked_from') ? 'message_id'
    AND NOT ((s.metadata->'forked_from') ? 'fork_message_id')
    AND NOT EXISTS (
      SELECT 1
      FROM bot_history_messages m
      WHERE m.session_id = s.id
        AND m.created_at > s.created_at
    )
)
UPDATE bot_sessions s
SET deleted_at = now(),
    updated_at = now(),
    metadata = jsonb_set(
      s.metadata,
      '{repair}',
      COALESCE(s.metadata->'repair', '{}'::jsonb) || jsonb_build_object('0105_incomplete_fork_session', true),
      true
    )
FROM incomplete_forks f
WHERE s.id = f.id;
