-- name: CreateCompactionLog :one
WITH owner_session AS MATERIALIZED (
  SELECT session.id, session.bot_id, session.compaction_epoch, session.team_id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = sqlc.arg(session_id)
    AND session.bot_id = sqlc.arg(bot_id)
    AND session.compaction_epoch = sqlc.arg(expected_epoch)
  FOR UPDATE
)
INSERT INTO bot_history_message_compacts (bot_id, session_id, compaction_epoch, team_id)
SELECT sqlc.arg(bot_id), owner_session.id, owner_session.compaction_epoch, owner_session.team_id
FROM owner_session
RETURNING id, bot_id, session_id, status, summary, message_count, error_message, usage, model_id,
          artifact_version, coverage, anchor_start_ms, anchor_end_ms, artifact_level, parent_ids,
          superseded_by, superseded_at, compaction_epoch, started_at, completed_at, team_id;

-- name: CompleteCompactionLog :one
WITH target_compact AS MATERIALIZED (
  SELECT compact.id, compact.session_id, compact.team_id
  FROM bot_history_message_compacts compact
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.id = $1
    AND compact.status = 'pending'
),
owner_session AS MATERIALIZED (
  SELECT session.id, session.bot_id, session.compaction_epoch, session.team_id
  FROM bot_sessions session
  JOIN target_compact compact
    ON compact.session_id = session.id
   AND compact.team_id = session.team_id
  WHERE session.team_id = public.memoh_current_team_id()
  FOR UPDATE OF session
),
locked_compact AS MATERIALIZED (
  SELECT compact.id, compact.team_id
  FROM bot_history_message_compacts compact
  JOIN target_compact target
    ON target.id = compact.id
   AND target.team_id = compact.team_id
  LEFT JOIN owner_session owner
    ON owner.id = target.session_id
   AND owner.team_id = target.team_id
  WHERE compact.team_id = public.memoh_current_team_id()
    AND (target.session_id IS NULL OR owner.id IS NOT NULL)
  FOR UPDATE OF compact
)
UPDATE bot_history_message_compacts compact
SET status = $2,
    summary = $3,
    message_count = $4,
    error_message = $5,
    usage = $6,
    model_id = $7,
    coverage = $8,
    anchor_start_ms = $9,
    anchor_end_ms = $10,
    completed_at = now()
FROM locked_compact locked
WHERE compact.team_id = public.memoh_current_team_id()
  AND compact.team_id = locked.team_id
  AND compact.id = locked.id
  AND compact.status = 'pending'
  AND (
    $2 <> 'ok'
    OR (
      (
        compact.session_id IS NULL
        OR EXISTS (
          SELECT 1
          FROM owner_session owner
          WHERE owner.team_id = compact.team_id
            AND owner.id = compact.session_id
            AND owner.bot_id = compact.bot_id
            AND owner.compaction_epoch = compact.compaction_epoch
        )
      )
      AND (
        SELECT count(*)
        FROM bot_history_messages source_message
        WHERE source_message.team_id = compact.team_id
          AND source_message.compact_id = compact.id
      ) = $4
    )
  )
RETURNING compact.id, compact.bot_id, compact.session_id, compact.status, compact.summary,
          compact.message_count, compact.error_message, compact.usage, compact.model_id,
          compact.artifact_version, compact.coverage, compact.anchor_start_ms, compact.anchor_end_ms,
          compact.artifact_level, compact.parent_ids, compact.superseded_by, compact.superseded_at,
          compact.compaction_epoch, compact.started_at, compact.completed_at, compact.team_id;

-- name: CompleteCompactionRollup :one
WITH target_compact AS MATERIALIZED (
  SELECT compact.id, compact.session_id, compact.team_id
  FROM bot_history_message_compacts compact
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.id = sqlc.arg(id)
    AND compact.status = 'pending'
),
owner_session AS MATERIALIZED (
  SELECT session.id, session.bot_id, session.compaction_epoch, session.team_id
  FROM bot_sessions session
  JOIN target_compact compact
    ON compact.session_id = session.id
   AND compact.team_id = session.team_id
  WHERE session.team_id = public.memoh_current_team_id()
  FOR UPDATE OF session
),
locked_compact AS MATERIALIZED (
  SELECT compact.id, compact.bot_id, compact.session_id, compact.compaction_epoch, compact.team_id
  FROM bot_history_message_compacts compact
  JOIN target_compact target
    ON target.id = compact.id
   AND target.team_id = compact.team_id
  LEFT JOIN owner_session owner
    ON owner.id = target.session_id
   AND owner.team_id = target.team_id
  WHERE compact.team_id = public.memoh_current_team_id()
    AND (target.session_id IS NULL OR owner.id IS NOT NULL)
    AND compact.status = 'pending'
  FOR UPDATE OF compact
),
locked_parents AS MATERIALIZED (
  SELECT parent.id, parent.team_id
  FROM bot_history_message_compacts parent
  JOIN locked_compact target
    ON target.team_id = parent.team_id
  JOIN owner_session owner
    ON owner.team_id = target.team_id
   AND owner.id = target.session_id
   AND owner.bot_id = target.bot_id
   AND owner.compaction_epoch = target.compaction_epoch
  WHERE parent.team_id = public.memoh_current_team_id()
    AND sqlc.arg(status)::text = 'ok'
    AND parent.id = ANY(sqlc.arg(parents)::uuid[])
    AND parent.bot_id = target.bot_id
    AND parent.session_id = target.session_id
    AND parent.compaction_epoch = owner.compaction_epoch
    AND parent.status = 'ok'
    AND parent.superseded_by IS NULL
  ORDER BY parent.id
  FOR UPDATE OF parent
),
eligible_completion AS MATERIALIZED (
  SELECT target.id, target.bot_id, target.session_id, target.compaction_epoch, target.team_id
  FROM locked_compact target
  WHERE sqlc.arg(status)::text <> 'ok'
     OR (
       EXISTS (
         SELECT 1
         FROM owner_session owner
         WHERE owner.team_id = target.team_id
           AND owner.id = target.session_id
           AND owner.bot_id = target.bot_id
           AND owner.compaction_epoch = target.compaction_epoch
       )
       AND (
         SELECT count(*)
         FROM bot_history_messages source_message
         WHERE source_message.team_id = target.team_id
           AND source_message.compact_id = target.id
       ) = sqlc.arg(message_count)
       AND (
         SELECT count(*)
         FROM locked_parents
       ) = CARDINALITY(sqlc.arg(parents)::uuid[])
     )
),
superseded_parents AS (
  UPDATE bot_history_message_compacts parent
  SET superseded_by = target.id,
      superseded_at = now()
  FROM locked_parents locked
  JOIN eligible_completion target
    ON target.team_id = locked.team_id
  WHERE sqlc.arg(status)::text = 'ok'
    AND parent.team_id = public.memoh_current_team_id()
    AND parent.team_id = locked.team_id
    AND parent.id = locked.id
    AND parent.bot_id = target.bot_id
    AND parent.session_id = target.session_id
    AND parent.compaction_epoch = target.compaction_epoch
    AND parent.status = 'ok'
    AND parent.superseded_by IS NULL
  RETURNING parent.id
),
superseded_parent_count AS MATERIALIZED (
  SELECT count(*) AS value
  FROM superseded_parents
)
UPDATE bot_history_message_compacts compact
SET status = sqlc.arg(status),
    summary = sqlc.arg(summary),
    message_count = sqlc.arg(message_count),
    error_message = sqlc.arg(error_message),
    usage = sqlc.arg(usage),
    model_id = sqlc.arg(model_id),
    coverage = sqlc.arg(coverage),
    anchor_start_ms = sqlc.arg(anchor_start_ms),
    anchor_end_ms = sqlc.arg(anchor_end_ms),
    artifact_level = sqlc.arg(level),
    parent_ids = sqlc.arg(parents)::uuid[],
    completed_at = now()
FROM eligible_completion eligible
CROSS JOIN superseded_parent_count parent_count
WHERE compact.team_id = public.memoh_current_team_id()
  AND compact.team_id = eligible.team_id
  AND compact.id = eligible.id
  AND compact.status = 'pending'
  AND (
    sqlc.arg(status)::text <> 'ok'
    OR parent_count.value = CARDINALITY(sqlc.arg(parents)::uuid[])
  )
RETURNING compact.id, compact.bot_id, compact.session_id, compact.status, compact.summary,
          compact.message_count, compact.error_message, compact.usage, compact.model_id,
          compact.artifact_version, compact.coverage, compact.anchor_start_ms, compact.anchor_end_ms,
          compact.artifact_level, compact.parent_ids, compact.superseded_by, compact.superseded_at,
          compact.compaction_epoch, compact.started_at, compact.completed_at, compact.team_id;

-- name: GetCompactionLogByID :one
SELECT id, bot_id, session_id, status, summary, message_count, error_message, usage, model_id,
       artifact_version, coverage, anchor_start_ms, anchor_end_ms, artifact_level, parent_ids,
       superseded_by, superseded_at, compaction_epoch, started_at, completed_at, team_id
FROM bot_history_message_compacts compact
WHERE compact.team_id = public.memoh_current_team_id()
  AND compact.id = $1
  AND (
    compact.session_id IS NULL
    OR EXISTS (
      SELECT 1
      FROM bot_sessions owner_session
      WHERE owner_session.team_id = compact.team_id
        AND owner_session.id = compact.session_id
        AND owner_session.bot_id = compact.bot_id
        AND owner_session.compaction_epoch = compact.compaction_epoch
    )
  );

-- name: ListCompactionArtifactParentIDsBySuccessor :many
SELECT parent.id
FROM bot_history_message_compacts parent
JOIN bot_sessions owner_session
  ON owner_session.id = sqlc.narg(session_id)::uuid
 AND owner_session.bot_id = sqlc.arg(bot_id)
 AND owner_session.team_id = parent.team_id
WHERE parent.team_id = public.memoh_current_team_id()
  AND parent.superseded_by = sqlc.arg(successor_id)
  AND parent.bot_id = owner_session.bot_id
  AND parent.session_id = owner_session.id
  AND parent.compaction_epoch = owner_session.compaction_epoch
  AND parent.status = 'ok'
  AND EXISTS (
    SELECT 1
    FROM bot_history_message_compacts successor
    WHERE successor.team_id = parent.team_id
      AND successor.id = parent.superseded_by
      AND successor.bot_id = owner_session.bot_id
      AND successor.session_id = owner_session.id
      AND successor.compaction_epoch = owner_session.compaction_epoch
  )
ORDER BY parent.id ASC;

-- name: ListCompactionLogsByBot :many
SELECT id, bot_id, session_id, status, summary, message_count, error_message, usage, model_id,
       artifact_version, coverage, anchor_start_ms, anchor_end_ms, artifact_level, parent_ids,
       superseded_by, superseded_at, compaction_epoch, started_at, completed_at, team_id
FROM bot_history_message_compacts
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1
ORDER BY started_at DESC
LIMIT $2 OFFSET $3;

-- name: ListCompactionLogsBySession :many
SELECT id, bot_id, session_id, status, summary, message_count, error_message, usage, model_id,
       artifact_version, coverage, anchor_start_ms, anchor_end_ms, artifact_level, parent_ids,
       superseded_by, superseded_at, compaction_epoch, started_at, completed_at, team_id
FROM bot_history_message_compacts
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1 AND session_id = $2
ORDER BY started_at DESC, id DESC
LIMIT $3;

-- name: ListCompactionLogsBySessionBefore :many
SELECT id, bot_id, session_id, status, summary, message_count, error_message, usage, model_id,
       artifact_version, coverage, anchor_start_ms, anchor_end_ms, artifact_level, parent_ids,
       superseded_by, superseded_at, compaction_epoch, started_at, completed_at, team_id
FROM bot_history_message_compacts
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1 AND session_id = $2
  AND (started_at, id) < (sqlc.arg(before_started_at)::timestamptz, sqlc.arg(before_id)::uuid)
ORDER BY started_at DESC, id DESC
LIMIT $3;

-- name: CountCompactionLogsByBot :one
SELECT count(*) FROM bot_history_message_compacts WHERE team_id = public.memoh_current_team_id() AND bot_id = $1;

-- name: ListCompactionArtifactLineageBySession :many
SELECT c.id, c.bot_id, c.session_id, c.status, c.summary, c.message_count, c.error_message, c.usage, c.model_id,
       c.artifact_version, c.coverage, c.anchor_start_ms, c.anchor_end_ms, c.artifact_level, c.parent_ids,
       c.superseded_by, c.superseded_at, c.compaction_epoch, c.started_at, c.completed_at, c.team_id
FROM bot_history_message_compacts c
JOIN bot_sessions owner_session
  ON owner_session.id = $1
 AND owner_session.bot_id = c.bot_id
 AND owner_session.team_id = c.team_id
WHERE c.team_id = public.memoh_current_team_id()
  AND c.session_id = owner_session.id
  AND c.compaction_epoch = owner_session.compaction_epoch
  AND (
    c.status = 'ok'
    OR EXISTS (
      SELECT 1
      FROM bot_history_message_compacts parent
      WHERE parent.team_id = c.team_id
        AND parent.bot_id = owner_session.bot_id
        AND parent.session_id = owner_session.id
        AND parent.compaction_epoch = owner_session.compaction_epoch
        AND parent.status = 'ok'
        AND parent.superseded_by = c.id
    )
  )
ORDER BY c.anchor_start_ms ASC, c.started_at ASC, c.id ASC;

-- name: DeleteCompactionLogsByBot :exec
WITH target_sessions AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.bot_id = sqlc.arg(target_bot_id)
  ORDER BY session.id
  FOR UPDATE
),
invalidated_sessions AS (
  UPDATE bot_sessions session
  SET compaction_epoch = session.compaction_epoch + 1
  FROM target_sessions target
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = target.id
  RETURNING session.id
),
target_compaction_logs AS MATERIALIZED (
  SELECT compact.id
  FROM bot_history_message_compacts compact
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.bot_id = sqlc.arg(target_bot_id)
    AND (SELECT count(*) FROM target_sessions) >= 0
  ORDER BY compact.id
  FOR UPDATE
)
DELETE FROM bot_history_message_compacts AS compacts
USING target_compaction_logs target
WHERE compacts.team_id = public.memoh_current_team_id()
  AND compacts.id = target.id;
