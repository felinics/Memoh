-- name: GetActiveHistoryCheckpoint :one
-- The session's active checkpoint; no row when its whole history is context.
SELECT checkpoint.*
FROM bot_history_checkpoints checkpoint
JOIN bot_sessions session
  ON session.team_id = checkpoint.team_id
 AND session.history_checkpoint_id = checkpoint.id
WHERE session.team_id = public.memoh_current_team_id()
  AND session.id = sqlc.arg(session_id);

-- name: GetHistoryCheckpoint :one
SELECT *
FROM bot_history_checkpoints
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id);

-- name: ListHistoryCheckpointSummaries :many
SELECT compact.id, compact.summary
FROM bot_history_message_compacts compact
WHERE compact.team_id = public.memoh_current_team_id()
  AND compact.session_id = sqlc.arg(session_id)
  AND compact.id = ANY(sqlc.arg(ids)::uuid[])
  AND compact.status = 'ok';

-- name: ListHistoryCheckpointSpanRows :many
-- The context rows a fold's checkpoint accounts for: the rows after its
-- parent's position through its own, and the rows named by ids (the rows its
-- parent keeps by reference and the rows it folds), in turn order.
SELECT m.id, m.turn_position, m.turn_message_seq
FROM bot_visible_history_messages m
WHERE m.team_id = public.memoh_current_team_id()
  AND m.session_id = sqlc.arg(session_id)
  AND (
    (
      (m.turn_position, m.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
      AND (m.turn_position, m.turn_message_seq) <= (sqlc.arg(through_turn_position)::bigint, sqlc.arg(through_turn_message_seq)::bigint)
    )
    OR m.id = ANY(sqlc.arg(ids)::uuid[])
  )
  AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC;

-- name: CreateHistoryCheckpoint :one
INSERT INTO bot_history_checkpoints (
  session_id, parent_id, turn_position, turn_message_seq, items
)
SELECT session.id, sqlc.narg(parent_id)::uuid,
  sqlc.arg(turn_position)::bigint, sqlc.arg(turn_message_seq)::bigint,
  sqlc.arg(items)::jsonb
FROM bot_sessions session
WHERE session.team_id = public.memoh_current_team_id()
  AND session.id = sqlc.arg(session_id)
RETURNING *;

-- name: SetHistoryCheckpoint :execrows
-- Moves the session's active checkpoint, only from the one the caller read.
UPDATE bot_sessions
SET history_checkpoint_id = sqlc.narg(checkpoint_id)::uuid
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(session_id)
  AND history_checkpoint_id IS NOT DISTINCT FROM sqlc.narg(expected_checkpoint_id)::uuid;

-- name: GetHistoryCheckpointKeptBeforeTurn :one
-- The checkpoint the session keeps once the turn's rows are replaced, which
-- an edit or retry renders before its replacement commits; NULL when none
-- holds. turn_found reports whether the turn has context rows to replace.
WITH replaced AS MATERIALIZED (
  SELECT MIN(m.turn_position)::bigint AS turn_position,
    MIN(m.turn_message_seq)::bigint AS turn_message_seq
  FROM bot_visible_history_messages m
  WHERE m.team_id = public.memoh_current_team_id()
    AND m.session_id = sqlc.arg(session_id)
    AND m.turn_id = sqlc.arg(turn_id)
    AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
)
SELECT (replaced.turn_position IS NOT NULL)::boolean AS turn_found,
  public.memoh_history_checkpoint_kept(sqlc.arg(session_id), replaced.turn_position, replaced.turn_message_seq)::uuid AS checkpoint_id
FROM replaced;

-- name: GetHistoryCheckpointKeptThroughTurn :one
-- The checkpoint a fork of the session through a turn takes: the newest on
-- the session's active chain that accounts for no history after that turn.
SELECT checkpoint.*
FROM bot_history_checkpoints checkpoint
WHERE checkpoint.team_id = public.memoh_current_team_id()
  AND checkpoint.id = public.memoh_history_checkpoint_kept(sqlc.arg(session_id), sqlc.arg(through_turn_position)::bigint + 1, 0);

-- name: RewindHistoryCheckpointBeforeTurn :one
-- An edit or retry that compacts before its first step commits rewinds the
-- session as SelectBoundHistoryTurnReplacement will when that step replaces
-- the turn, so its fold builds on the checkpoint the replacement keeps. It
-- reports whether it rewound.
WITH owner_session AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = sqlc.arg(session_id)
    AND session.bot_id = sqlc.arg(bot_id)
  FOR UPDATE
), replaced AS MATERIALIZED (
  SELECT MIN(m.turn_position)::bigint AS turn_position,
    MIN(m.turn_message_seq)::bigint AS turn_message_seq
  FROM bot_visible_history_messages m
  JOIN owner_session owner ON owner.id = m.session_id
  WHERE m.team_id = public.memoh_current_team_id()
    AND m.turn_id = sqlc.arg(turn_id)
    AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
)
SELECT COALESCE((
  SELECT public.memoh_rewind_history_checkpoint(owner.id, replaced.turn_position, replaced.turn_message_seq)
  FROM replaced
  WHERE replaced.turn_position IS NOT NULL
), false)::boolean AS rewound
FROM owner_session owner;

-- name: ListHistoryCheckpointCandidatesWithinBytes :many
-- A compaction pass reads its candidates oldest first within a hard content
-- budget: the rows the checkpoint it folds onto keeps by reference and the
-- context rows after that checkpoint's position, before the cutoff position
-- when set. A read takes whole call groups, a row other than a tool's and the
-- tool rows after it that carry its calls' results, so no pass folds a call
-- apart from a result the read left out: every group that ends within the
-- budget, and the group that crosses it when what it reads past the budget
-- fits max_row_bytes, so every pass can read past what it has read. The
-- cumulative filter is evaluated before the payload join, so a read never
-- loads more than max_bytes and max_row_bytes together. CandidateCount
-- reports whether the prefix was truncated.
WITH candidate_rows AS MATERIALIZED (
  SELECT
    m.id,
    m.turn_position,
    m.turn_message_seq,
    m.created_at,
    (
      octet_length(m.content::text)
    )::BIGINT AS payload_bytes,
    (m.role <> 'tool')::int AS opens_group
  FROM bot_visible_history_messages m
  WHERE m.team_id = public.memoh_current_team_id()
    AND m.session_id = sqlc.arg(session_id)
    AND (
      (m.turn_position, m.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
      OR m.id = ANY(sqlc.arg(kept_ids)::uuid[])
    )
    AND (sqlc.narg(before_turn_position)::bigint IS NULL
      OR (m.turn_position, m.turn_message_seq) < (sqlc.narg(before_turn_position)::bigint, sqlc.narg(before_turn_message_seq)::bigint))
    AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
), ranked_candidates AS MATERIALIZED (
  SELECT
    candidate_rows.*,
    COUNT(*) OVER ()::BIGINT AS candidate_count,
    SUM(payload_bytes) OVER history_order::BIGINT AS cumulative_bytes,
    SUM(opens_group) OVER history_order::BIGINT AS call_group
  FROM candidate_rows
  WINDOW history_order AS (ORDER BY turn_position ASC, turn_message_seq ASC, created_at ASC, id ASC)
), grouped_candidates AS MATERIALIZED (
  SELECT
    ranked_candidates.*,
    MAX(cumulative_bytes) OVER (PARTITION BY call_group)::BIGINT AS group_end_bytes
  FROM ranked_candidates
), crossing_group AS (
  SELECT MIN(call_group) AS call_group
  FROM grouped_candidates
  WHERE group_end_bytes > sqlc.arg(max_bytes)::BIGINT
), admitted_candidates AS MATERIALIZED (
  SELECT grouped.*
  FROM grouped_candidates grouped
  CROSS JOIN crossing_group
  WHERE grouped.group_end_bytes <= sqlc.arg(max_bytes)::BIGINT
    OR (grouped.call_group = crossing_group.call_group
      AND grouped.group_end_bytes - sqlc.arg(max_bytes)::BIGINT <= sqlc.arg(max_row_bytes)::BIGINT)
)
SELECT
  m.id,
  m.source_message_id AS external_message_id,
  m.role,
  m.content,
  m.turn_position,
  m.turn_message_seq,
  admitted.candidate_count
FROM bot_visible_history_messages m
JOIN admitted_candidates admitted ON admitted.id = m.id
WHERE m.team_id = public.memoh_current_team_id()
ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC;

-- name: CountHistoryCheckpointCandidates :one
-- Metadata-only companion of ListHistoryCheckpointCandidatesWithinBytes over
-- the same candidates. It never returns payloads: it counts them, which tells
-- a read that took nothing whether rows are left.
SELECT COUNT(*)::BIGINT AS candidate_count
FROM bot_visible_history_messages m
WHERE m.team_id = public.memoh_current_team_id()
  AND m.session_id = sqlc.arg(session_id)
  AND (
    (m.turn_position, m.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
    OR m.id = ANY(sqlc.arg(kept_ids)::uuid[])
  )
  AND (sqlc.narg(before_turn_position)::bigint IS NULL
    OR (m.turn_position, m.turn_message_seq) < (sqlc.narg(before_turn_position)::bigint, sqlc.narg(before_turn_message_seq)::bigint))
  AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync');

-- name: ListHistoryMessagesAfterWithinBytes :many
-- The raw history after a checkpoint (CM-ADM-001): the context rows after its
-- position, before the cutoff position when set, admitted newest-first until
-- their running content byte total crosses max_bytes (the crossing row is
-- kept, so the newest row always loads) and returned in ascending order. The
-- walk follows the visible-session-order index and stops at the budget, so
-- older rows are never measured, however much history the checkpoint leaves
-- raw. truncated reports that the budget, not the window, ended the walk:
-- context rows of the window remain older than the oldest it admitted. Only
-- content is budgeted: full rows, metadata included, still load, so callers
-- that need content alone use ListTurnResponseSourcesAfterWithinBytes.
WITH RECURSIVE history_window AS (
  (
    SELECT m.id, m.turn_position, m.turn_message_seq, m.created_at,
      octet_length(m.content::text)::bigint AS total_bytes
    FROM bot_history_messages m
    WHERE m.team_id = public.memoh_current_team_id()
      AND m.session_id = sqlc.arg(session_id)
      AND m.turn_visible = true
      AND m.turn_id IS NOT NULL
      AND m.turn_position IS NOT NULL
      AND m.turn_message_seq IS NOT NULL
      AND (m.turn_position, m.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
      AND (sqlc.narg(before_turn_position)::bigint IS NULL
        OR (m.turn_position, m.turn_message_seq) < (sqlc.narg(before_turn_position)::bigint, sqlc.narg(before_turn_message_seq)::bigint))
      AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
      AND sqlc.arg(max_bytes)::bigint > 0
    ORDER BY m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
    LIMIT 1
  )
  UNION ALL
  SELECT older.id, older.turn_position, older.turn_message_seq, older.created_at,
    previous.total_bytes + older.content_bytes AS total_bytes
  FROM history_window previous
  CROSS JOIN LATERAL (
    SELECT m.id, m.turn_position, m.turn_message_seq, m.created_at,
      octet_length(m.content::text)::bigint AS content_bytes
    FROM bot_history_messages m
    WHERE m.team_id = public.memoh_current_team_id()
      AND m.session_id = sqlc.arg(session_id)
      AND m.turn_visible = true
      AND m.turn_id IS NOT NULL
      AND m.turn_position IS NOT NULL
      AND m.turn_message_seq IS NOT NULL
      AND (m.turn_position, m.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
      AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
      AND (m.turn_position, m.turn_message_seq, m.created_at, m.id)
        < (previous.turn_position, previous.turn_message_seq, previous.created_at, previous.id)
    ORDER BY m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
    LIMIT 1
  ) older
  WHERE previous.total_bytes < sqlc.arg(max_bytes)::bigint
), oldest AS (
  SELECT admitted.turn_position, admitted.turn_message_seq, admitted.created_at, admitted.id
  FROM history_window admitted
  ORDER BY admitted.turn_position ASC, admitted.turn_message_seq ASC, admitted.created_at ASC, admitted.id ASC
  LIMIT 1
), budget_cut AS (
  SELECT EXISTS (
    SELECT 1
    FROM oldest
    JOIN bot_history_messages m
      ON m.team_id = public.memoh_current_team_id()
     AND m.session_id = sqlc.arg(session_id)
     AND (m.turn_position, m.turn_message_seq, m.created_at, m.id)
       < (oldest.turn_position, oldest.turn_message_seq, oldest.created_at, oldest.id)
    WHERE m.turn_visible = true
      AND m.turn_id IS NOT NULL
      AND m.turn_position IS NOT NULL
      AND m.turn_message_seq IS NOT NULL
      AND (m.turn_position, m.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
      AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
  ) AS truncated
)
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform,
  budget_cut.truncated
FROM history_window admitted
JOIN bot_history_messages m ON m.id = admitted.id
  AND m.team_id = public.memoh_current_team_id()
  AND m.session_id = sqlc.arg(session_id)
CROSS JOIN budget_cut
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
ORDER BY admitted.turn_position ASC, admitted.turn_message_seq ASC, admitted.created_at ASC, admitted.id ASC;

-- name: ListHistoryRowsBySources :many
-- The context rows a fold reads, named by row id or by the inbound message
-- they record, at their places in the history.
SELECT m.id, m.source_message_id, m.turn_position, m.turn_message_seq
FROM bot_visible_history_messages m
WHERE m.team_id = public.memoh_current_team_id()
  AND m.session_id = sqlc.arg(session_id)
  AND (m.id = ANY(sqlc.arg(ids)::uuid[]) OR m.source_message_id = ANY(sqlc.arg(external_message_ids)::text[]))
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL
  AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC;

-- name: ListHistoryMessagesByIDs :many
-- The rows a checkpoint keeps by reference, as context rows, in turn order.
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_visible_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id()
  AND m.session_id = sqlc.arg(session_id)
  AND m.id = ANY(sqlc.arg(ids)::uuid[])
  AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC;

-- name: MeasureHistoryMessagesAfter :one
-- Metadata-only context admission measure (CM-ADM-001) of the raw history
-- after a checkpoint: counts and content bytes aggregate on the database
-- side, so admission can size it without shipping any payload.
SELECT
  COUNT(*)::BIGINT AS message_count,
  COALESCE(SUM(octet_length(m.content::text)), 0)::BIGINT AS content_bytes
FROM bot_visible_history_messages m
WHERE m.team_id = public.memoh_current_team_id()
  AND m.session_id = sqlc.arg(session_id)
  AND (m.turn_position, m.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
  AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync');

-- name: ListTurnResponseSourcesAfterWithinBytes :many
-- Turn-response (TR) composition, in Discuss and in pipeline chat, only needs
-- content, the interrupted-checkpoint flag and the Discuss step flag. Never
-- return full metadata: legacy context_lifecycle snapshots can dwarf the
-- content budget. Walk as ListHistoryMessagesAfterWithinBytes walks,
-- including its crossing row, turn order and truncated.
WITH RECURSIVE history_window AS (
  (
    SELECT m.id, m.turn_position, m.turn_message_seq, m.created_at,
      octet_length(m.content::text)::bigint AS total_bytes
    FROM bot_history_messages m
    WHERE m.team_id = public.memoh_current_team_id()
      AND m.session_id = sqlc.arg(session_id)
      AND m.turn_visible = true
      AND m.turn_id IS NOT NULL
      AND m.turn_position IS NOT NULL
      AND m.turn_message_seq IS NOT NULL
      AND (m.turn_position, m.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
      AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
      AND sqlc.arg(max_bytes)::bigint > 0
    ORDER BY m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
    LIMIT 1
  )
  UNION ALL
  SELECT older.id, older.turn_position, older.turn_message_seq, older.created_at,
    previous.total_bytes + older.content_bytes AS total_bytes
  FROM history_window previous
  CROSS JOIN LATERAL (
    SELECT m.id, m.turn_position, m.turn_message_seq, m.created_at,
      octet_length(m.content::text)::bigint AS content_bytes
    FROM bot_history_messages m
    WHERE m.team_id = public.memoh_current_team_id()
      AND m.session_id = sqlc.arg(session_id)
      AND m.turn_visible = true
      AND m.turn_id IS NOT NULL
      AND m.turn_position IS NOT NULL
      AND m.turn_message_seq IS NOT NULL
      AND (m.turn_position, m.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
      AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
      AND (m.turn_position, m.turn_message_seq, m.created_at, m.id)
        < (previous.turn_position, previous.turn_message_seq, previous.created_at, previous.id)
    ORDER BY m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
    LIMIT 1
  ) older
  WHERE previous.total_bytes < sqlc.arg(max_bytes)::bigint
), oldest AS (
  SELECT admitted.turn_position, admitted.turn_message_seq, admitted.created_at, admitted.id
  FROM history_window admitted
  ORDER BY admitted.turn_position ASC, admitted.turn_message_seq ASC, admitted.created_at ASC, admitted.id ASC
  LIMIT 1
), budget_cut AS (
  SELECT EXISTS (
    SELECT 1
    FROM oldest
    JOIN bot_history_messages m
      ON m.team_id = public.memoh_current_team_id()
     AND m.session_id = sqlc.arg(session_id)
     AND (m.turn_position, m.turn_message_seq, m.created_at, m.id)
       < (oldest.turn_position, oldest.turn_message_seq, oldest.created_at, oldest.id)
    WHERE m.turn_visible = true
      AND m.turn_id IS NOT NULL
      AND m.turn_position IS NOT NULL
      AND m.turn_message_seq IS NOT NULL
      AND (m.turn_position, m.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
      AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
  ) AS truncated
)
SELECT m.id, m.role, m.content, m.created_at,
  COALESCE(m.metadata->'agent_step_interrupted' = 'true'::jsonb, false)::boolean AS interrupted,
  COALESCE(m.metadata->'discuss_step' = 'true'::jsonb, false)::boolean AS discuss_step,
  budget_cut.truncated
FROM history_window admitted
JOIN bot_history_messages m ON m.id = admitted.id
  AND m.team_id = public.memoh_current_team_id()
  AND m.session_id = sqlc.arg(session_id)
CROSS JOIN budget_cut
ORDER BY admitted.turn_position ASC, admitted.turn_message_seq ASC, admitted.created_at ASC, admitted.id ASC;

-- name: ListTurnResponseSourcesByIDs :many
-- The turn-response projection of the rows a checkpoint keeps by reference,
-- with the inbound message a user row records: timeline composition renders
-- that message's segment for it.
SELECT
  m.id,
  m.role,
  m.content,
  m.created_at,
  COALESCE(m.metadata->'agent_step_interrupted' = 'true'::jsonb, false)::boolean AS interrupted,
  COALESCE(m.metadata->'discuss_step' = 'true'::jsonb, false)::boolean AS discuss_step,
  m.source_message_id AS external_message_id
FROM bot_visible_history_messages m
WHERE m.team_id = public.memoh_current_team_id()
  AND m.session_id = sqlc.arg(session_id)
  AND m.id = ANY(sqlc.arg(ids)::uuid[])
  AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC;

-- name: ClassifyHistoryCheckpointMessages :many
-- How a checkpoint stands for each inbound message that has context rows:
-- kept when one of its rows is after the checkpoint's position or is a row
-- the checkpoint keeps by reference, folded otherwise. event_cursor is the
-- cursor of the event its rows were written from, 0 when unknown: a summary
-- reads rows, so it knows the message as of that event. A message without
-- context rows returns nothing.
SELECT
  m.source_message_id::text AS external_message_id,
  bool_or(
    (m.turn_position, m.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
    OR m.id = ANY(sqlc.arg(kept_ids)::uuid[])
  )::boolean AS kept,
  COALESCE(MAX((event.event_data->>'event_cursor')::bigint), 0)::bigint AS event_cursor
FROM bot_visible_history_messages m
LEFT JOIN bot_session_events event
  ON event.team_id = public.memoh_current_team_id()
 AND event.id = m.event_id
WHERE m.team_id = public.memoh_current_team_id()
  AND m.session_id = sqlc.arg(session_id)
  AND m.source_message_id = ANY(sqlc.arg(external_message_ids)::text[])
  AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
GROUP BY m.source_message_id;

-- name: GetForkHistoryPosition :one
-- The fork's position of the newest row it copied from at or before a
-- source position.
WITH pair AS (
  SELECT
    unnest(sqlc.arg(source_message_ids)::uuid[]) AS source_id,
    unnest(sqlc.arg(fork_message_ids)::uuid[]) AS fork_id
)
SELECT copy.turn_position::bigint AS turn_position, copy.turn_message_seq::bigint AS turn_message_seq
FROM pair
JOIN bot_history_messages source
  ON source.team_id = public.memoh_current_team_id()
 AND source.id = pair.source_id
JOIN bot_history_messages copy
  ON copy.team_id = public.memoh_current_team_id()
 AND copy.id = pair.fork_id
WHERE source.turn_position IS NOT NULL
  AND source.turn_message_seq IS NOT NULL
  AND copy.turn_position IS NOT NULL
  AND copy.turn_message_seq IS NOT NULL
  AND (source.turn_position, source.turn_message_seq) <= (sqlc.arg(turn_position)::bigint, sqlc.arg(turn_message_seq)::bigint)
ORDER BY source.turn_position DESC, source.turn_message_seq DESC
LIMIT 1;
