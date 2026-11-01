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
-- Writes a checkpoint after parent only while parent is the session's active
-- one, locking the session as the rewind that would move it does: a rewind
-- deletes the checkpoints it moves back past, parent among them. No row when
-- the session moved on.
INSERT INTO bot_history_checkpoints (
  session_id, parent_id, turn_position, turn_message_seq, items
)
SELECT session.id, sqlc.narg(parent_id)::uuid,
  sqlc.arg(turn_position)::bigint, sqlc.arg(turn_message_seq)::bigint,
  sqlc.arg(items)::jsonb
FROM bot_sessions session
WHERE session.team_id = public.memoh_current_team_id()
  AND session.id = sqlc.arg(session_id)
  AND session.history_checkpoint_id IS NOT DISTINCT FROM sqlc.narg(parent_id)::uuid
FOR UPDATE OF session
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
-- fits max_row_bytes, so every pass can read past what it has read. The read
-- walks the candidates in turn order on the context-order index, one row a
-- step, and stops at the end of the crossing group, or as soon as the group
-- is past max_row_bytes, so it measures (detoasts) no row it could not take,
-- however long the backlog: a catch-up that reads the backlog a pass at a
-- time measures each row once. truncated reports
-- that candidates are left after the admitted prefix. The kept rows the
-- checkpoint's position does not already cover are older than every row
-- after it, so the walk takes them first.
WITH RECURSIVE kept_rows AS MATERIALIZED (
  SELECT m.session_id, m.id, m.turn_position, m.turn_message_seq, m.created_at, m.role, octet_length(m.content::text)::bigint AS content_bytes
  FROM bot_history_messages m
  WHERE m.team_id = public.memoh_current_team_id()
    AND m.session_id = sqlc.arg(session_id)
    AND m.turn_visible = true
    AND m.turn_id IS NOT NULL
    AND m.turn_position IS NOT NULL
    AND m.turn_message_seq IS NOT NULL
    AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
    AND m.id = ANY(sqlc.arg(kept_ids)::uuid[])
    AND (m.turn_position, m.turn_message_seq) <= (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
    AND (sqlc.narg(before_turn_position)::bigint IS NULL
      OR (m.turn_position, m.turn_message_seq) < (sqlc.narg(before_turn_position)::bigint, sqlc.narg(before_turn_message_seq)::bigint))
), candidate_walk AS (
  (
    -- The kept rows were measured with their lookup; a row after the
    -- position is measured once the walk takes it, outside the LIMIT.
    SELECT first.session_id, first.id, first.turn_position, first.turn_message_seq, first.created_at,
      COALESCE(first.known_bytes, octet_length(first.content::text)::bigint) AS cumulative_bytes,
      (first.role <> 'tool')::int::bigint AS call_group,
      (first.turn_position, first.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint) AS past_position
    FROM (
      (
        SELECT kept.session_id, kept.id, kept.turn_position, kept.turn_message_seq, kept.created_at, kept.role, NULL::jsonb AS content, kept.content_bytes AS known_bytes
        FROM kept_rows kept
        ORDER BY kept.turn_position ASC, kept.turn_message_seq ASC, kept.created_at ASC, kept.id ASC
        LIMIT 1
      )
      UNION ALL
      (
        SELECT m.session_id, m.id, m.turn_position, m.turn_message_seq, m.created_at, m.role, m.content, NULL::bigint AS known_bytes
        FROM bot_history_messages m
        WHERE m.team_id = public.memoh_current_team_id()
          AND m.session_id = sqlc.arg(session_id)
          AND m.turn_visible = true
          AND m.turn_id IS NOT NULL
          AND m.turn_position IS NOT NULL
          AND m.turn_message_seq IS NOT NULL
          AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
          AND (m.turn_position, m.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
          AND (sqlc.narg(before_turn_position)::bigint IS NULL
            OR (m.turn_position, m.turn_message_seq) < (sqlc.narg(before_turn_position)::bigint, sqlc.narg(before_turn_message_seq)::bigint))
        ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC
        LIMIT 1
      )
    ) first
    ORDER BY first.turn_position ASC, first.turn_message_seq ASC, first.created_at ASC, first.id ASC
    LIMIT 1
  )
  UNION ALL
  SELECT next.session_id, next.id, next.turn_position, next.turn_message_seq, next.created_at,
    previous.cumulative_bytes + COALESCE(next.known_bytes, octet_length(next.content::text)::bigint) AS cumulative_bytes,
    previous.call_group + (next.role <> 'tool')::int::bigint AS call_group,
    (next.turn_position, next.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint) AS past_position
  FROM candidate_walk previous
  CROSS JOIN LATERAL (
    SELECT later.session_id, later.id, later.turn_position, later.turn_message_seq, later.created_at, later.role,
      later.content, later.known_bytes
    FROM (
      (
        SELECT kept.session_id, kept.id, kept.turn_position, kept.turn_message_seq, kept.created_at, kept.role, NULL::jsonb AS content, kept.content_bytes AS known_bytes
        FROM kept_rows kept
        WHERE NOT previous.past_position
          AND (kept.turn_position, kept.turn_message_seq, kept.created_at, kept.id)
            > (previous.turn_position, previous.turn_message_seq, previous.created_at, previous.id)
        ORDER BY kept.turn_position ASC, kept.turn_message_seq ASC, kept.created_at ASC, kept.id ASC
        LIMIT 1
      )
      UNION ALL
      (
        SELECT m.session_id, m.id, m.turn_position, m.turn_message_seq, m.created_at, m.role, m.content, NULL::bigint
        FROM bot_history_messages m
        WHERE m.team_id = public.memoh_current_team_id()
          AND m.turn_visible = true
          AND m.turn_id IS NOT NULL
          AND m.turn_position IS NOT NULL
          AND m.turn_message_seq IS NOT NULL
          AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
          -- The context row after a single lower bound in the context-order
          -- index, of whichever session, as the history walk steps: the
          -- previous row once the walk is past the position, the position
          -- itself before that (no row at the position sorts after infinity
          -- and the largest uuid). The walk ends at a row of another session
          -- or at the cutoff.
          AND (m.session_id, m.turn_position, m.turn_message_seq, m.created_at, m.id) > (
            previous.session_id,
            CASE WHEN previous.past_position THEN previous.turn_position ELSE sqlc.arg(after_turn_position)::bigint END,
            CASE WHEN previous.past_position THEN previous.turn_message_seq ELSE sqlc.arg(after_turn_message_seq)::bigint END,
            CASE WHEN previous.past_position THEN previous.created_at ELSE 'infinity'::timestamptz END,
            CASE WHEN previous.past_position THEN previous.id ELSE 'ffffffff-ffff-ffff-ffff-ffffffffffff'::uuid END
          )
        ORDER BY m.session_id ASC, m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC
        LIMIT 1
      )
    ) later
    ORDER BY later.session_id ASC, later.turn_position ASC, later.turn_message_seq ASC, later.created_at ASC, later.id ASC
    LIMIT 1
  ) next
  -- Within the budget every next row is read; past it only the crossing
  -- group's own tool rows, while what it reads past the budget still fits.
  -- A row is measured here, once the walk has taken it.
  WHERE (previous.cumulative_bytes <= sqlc.arg(max_bytes)::bigint
      OR (next.role = 'tool'
        AND previous.cumulative_bytes - sqlc.arg(max_bytes)::bigint <= sqlc.arg(max_row_bytes)::bigint))
    AND next.session_id = sqlc.arg(session_id)
    AND (sqlc.narg(before_turn_position)::bigint IS NULL
      OR (next.turn_position, next.turn_message_seq) < (sqlc.narg(before_turn_position)::bigint, sqlc.narg(before_turn_message_seq)::bigint))
), crossing_group AS MATERIALIZED (
  -- The group of the first row past the budget; the walk read all of it
  -- unless it stopped once the group was past max_row_bytes.
  SELECT walked.call_group, MAX(walked.cumulative_bytes) AS end_bytes
  FROM candidate_walk walked
  WHERE walked.call_group = (
    SELECT MIN(past.call_group)
    FROM candidate_walk past
    WHERE past.cumulative_bytes > sqlc.arg(max_bytes)::bigint
  )
  GROUP BY walked.call_group
), admitted_candidates AS MATERIALIZED (
  SELECT walked.id, walked.turn_position, walked.turn_message_seq, walked.created_at
  FROM candidate_walk walked
  LEFT JOIN crossing_group crossing ON true
  WHERE crossing.call_group IS NULL
    OR walked.call_group < crossing.call_group
    OR (walked.call_group = crossing.call_group
      AND crossing.end_bytes - sqlc.arg(max_bytes)::bigint <= sqlc.arg(max_row_bytes)::bigint)
), last_walked AS MATERIALIZED (
  SELECT walked.turn_position, walked.turn_message_seq, walked.created_at, walked.id, walked.past_position
  FROM candidate_walk walked
  ORDER BY walked.turn_position DESC, walked.turn_message_seq DESC, walked.created_at DESC, walked.id DESC
  LIMIT 1
), read_cut AS (
  SELECT (
    (SELECT COUNT(*) FROM candidate_walk) > (SELECT COUNT(*) FROM admitted_candidates)
    OR EXISTS (
      SELECT 1
      FROM last_walked last
      JOIN kept_rows kept
        ON (kept.turn_position, kept.turn_message_seq, kept.created_at, kept.id)
          > (last.turn_position, last.turn_message_seq, last.created_at, last.id)
    )
    OR EXISTS (
      SELECT 1
      FROM last_walked last
      JOIN bot_history_messages m
        ON m.team_id = public.memoh_current_team_id()
       AND m.session_id = sqlc.arg(session_id)
       -- The walk step's lower bound, after the last row it read.
       AND (m.turn_position, m.turn_message_seq, m.created_at, m.id) > (
         CASE WHEN last.past_position THEN last.turn_position ELSE sqlc.arg(after_turn_position)::bigint END,
         CASE WHEN last.past_position THEN last.turn_message_seq ELSE sqlc.arg(after_turn_message_seq)::bigint END,
         CASE WHEN last.past_position THEN last.created_at ELSE 'infinity'::timestamptz END,
         CASE WHEN last.past_position THEN last.id ELSE 'ffffffff-ffff-ffff-ffff-ffffffffffff'::uuid END
       )
      WHERE m.turn_visible = true
        AND m.turn_id IS NOT NULL
        AND m.turn_position IS NOT NULL
        AND m.turn_message_seq IS NOT NULL
        AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
        AND (sqlc.narg(before_turn_position)::bigint IS NULL
          OR (m.turn_position, m.turn_message_seq) < (sqlc.narg(before_turn_position)::bigint, sqlc.narg(before_turn_message_seq)::bigint))
    )
  )::boolean AS truncated
)
SELECT
  m.id,
  m.source_message_id AS external_message_id,
  m.role,
  m.content,
  m.turn_position,
  m.turn_message_seq,
  read_cut.truncated
FROM admitted_candidates admitted
JOIN bot_history_messages m ON m.id = admitted.id
  AND m.team_id = public.memoh_current_team_id()
CROSS JOIN read_cut
ORDER BY admitted.turn_position ASC, admitted.turn_message_seq ASC, admitted.created_at ASC, admitted.id ASC;

-- name: HasHistoryCheckpointCandidates :one
-- Metadata-only companion of ListHistoryCheckpointCandidatesWithinBytes over
-- the same candidates. It never returns payloads: it tells a read that took
-- nothing whether rows are left.
SELECT (
  EXISTS (
    SELECT 1
    FROM bot_history_messages m
    WHERE m.team_id = public.memoh_current_team_id()
      AND m.session_id = sqlc.arg(session_id)
      AND m.turn_visible = true
      AND m.turn_id IS NOT NULL
      AND m.turn_position IS NOT NULL
      AND m.turn_message_seq IS NOT NULL
      AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
      AND (m.turn_position, m.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
      AND (sqlc.narg(before_turn_position)::bigint IS NULL
        OR (m.turn_position, m.turn_message_seq) < (sqlc.narg(before_turn_position)::bigint, sqlc.narg(before_turn_message_seq)::bigint))
  )
  OR EXISTS (
    SELECT 1
    FROM bot_history_messages m
    WHERE m.team_id = public.memoh_current_team_id()
      AND m.session_id = sqlc.arg(session_id)
      AND m.turn_visible = true
      AND m.turn_id IS NOT NULL
      AND m.turn_position IS NOT NULL
      AND m.turn_message_seq IS NOT NULL
      AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
      AND m.id = ANY(sqlc.arg(kept_ids)::uuid[])
      AND (sqlc.narg(before_turn_position)::bigint IS NULL
        OR (m.turn_position, m.turn_message_seq) < (sqlc.narg(before_turn_position)::bigint, sqlc.narg(before_turn_message_seq)::bigint))
  )
)::boolean AS has_candidates;

-- name: ListHistoryMessagesAfterWithinBytes :many
-- The raw history after a checkpoint (CM-ADM-001): the context rows after its
-- position, before the cutoff position when set, admitted newest-first until
-- their running content byte total crosses max_bytes (the crossing row is
-- kept, so the newest row always loads) and returned in ascending order. The
-- walk follows the context-order index one row a step and stops at the
-- budget, so older rows are never measured, however much history the
-- checkpoint leaves raw. truncated reports that the budget, not the window, ended the walk:
-- context rows of the window remain older than the oldest it admitted. Only
-- content is budgeted: full rows, metadata included, still load, so callers
-- that need content alone use ListTurnResponseSourcesAfterWithinBytes.
WITH RECURSIVE history_window AS (
  -- Measured outside its LIMIT, so whatever plan finds the newest row
  -- measures that row alone.
  SELECT newest.session_id, newest.id, newest.turn_position, newest.turn_message_seq, newest.created_at,
    octet_length(newest.content::text)::bigint AS total_bytes
  FROM (
    SELECT m.session_id, m.id, m.turn_position, m.turn_message_seq, m.created_at, m.content
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
  ) newest
  UNION ALL
  SELECT older.session_id, older.id, older.turn_position, older.turn_message_seq, older.created_at,
    previous.total_bytes + octet_length(older.content::text)::bigint AS total_bytes
  FROM history_window previous
  CROSS JOIN LATERAL (
    -- The context row before the previous one in the context-order index,
    -- of whichever session: bounded by the previous row's own values rather
    -- than by the session, the step is planned as the next entry of a range
    -- and never as a scan and sort of a session the statistics may put at
    -- one row. The walk ends at a row of another session or of the
    -- checkpoint's span.
    SELECT m.session_id, m.id, m.turn_position, m.turn_message_seq, m.created_at, m.content
    FROM bot_history_messages m
    WHERE m.team_id = public.memoh_current_team_id()
      AND m.turn_visible = true
      AND m.turn_id IS NOT NULL
      AND m.turn_position IS NOT NULL
      AND m.turn_message_seq IS NOT NULL
      AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
      AND (m.session_id, m.turn_position, m.turn_message_seq, m.created_at, m.id)
        < (previous.session_id, previous.turn_position, previous.turn_message_seq, previous.created_at, previous.id)
    ORDER BY m.session_id DESC, m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
    LIMIT 1
  ) older
  WHERE previous.total_bytes < sqlc.arg(max_bytes)::bigint
    AND older.session_id = sqlc.arg(session_id)
    AND (older.turn_position, older.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
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
-- By id alone: the walk read these rows from the session, and a session
-- predicate here would let a plan reach the payloads through the session's
-- rows rather than the admitted ones.
JOIN bot_history_messages m ON m.id = admitted.id
  AND m.team_id = public.memoh_current_team_id()
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
  -- Measured outside its LIMIT, so whatever plan finds the newest row
  -- measures that row alone.
  SELECT newest.session_id, newest.id, newest.turn_position, newest.turn_message_seq, newest.created_at,
    octet_length(newest.content::text)::bigint AS total_bytes
  FROM (
    SELECT m.session_id, m.id, m.turn_position, m.turn_message_seq, m.created_at, m.content
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
  ) newest
  UNION ALL
  SELECT older.session_id, older.id, older.turn_position, older.turn_message_seq, older.created_at,
    previous.total_bytes + octet_length(older.content::text)::bigint AS total_bytes
  FROM history_window previous
  CROSS JOIN LATERAL (
    -- The context row before the previous one in the context-order index,
    -- of whichever session: bounded by the previous row's own values rather
    -- than by the session, the step is planned as the next entry of a range
    -- and never as a scan and sort of a session the statistics may put at
    -- one row. The walk ends at a row of another session or of the
    -- checkpoint's span.
    SELECT m.session_id, m.id, m.turn_position, m.turn_message_seq, m.created_at, m.content
    FROM bot_history_messages m
    WHERE m.team_id = public.memoh_current_team_id()
      AND m.turn_visible = true
      AND m.turn_id IS NOT NULL
      AND m.turn_position IS NOT NULL
      AND m.turn_message_seq IS NOT NULL
      AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
      AND (m.session_id, m.turn_position, m.turn_message_seq, m.created_at, m.id)
        < (previous.session_id, previous.turn_position, previous.turn_message_seq, previous.created_at, previous.id)
    ORDER BY m.session_id DESC, m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
    LIMIT 1
  ) older
  WHERE previous.total_bytes < sqlc.arg(max_bytes)::bigint
    AND older.session_id = sqlc.arg(session_id)
    AND (older.turn_position, older.turn_message_seq) > (sqlc.arg(after_turn_position)::bigint, sqlc.arg(after_turn_message_seq)::bigint)
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
-- By id alone: the walk read these rows from the session, and a session
-- predicate here would let a plan reach the payloads through the session's
-- rows rather than the admitted ones.
JOIN bot_history_messages m ON m.id = admitted.id
  AND m.team_id = public.memoh_current_team_id()
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
