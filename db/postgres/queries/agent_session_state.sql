-- name: GetRuntimeConfigEpoch :one
-- A handle records this pair before its process starts. Bound resolution and
-- every prompt compare it again, so a reset on another server invalidates
-- idle and unbound processes that have no active session_run to route to.
SELECT
  bot.runtime_config_epoch AS bot_runtime_config_epoch,
  COALESCE(session.runtime_config_epoch, 0)::bigint AS session_runtime_config_epoch
FROM bots bot
LEFT JOIN bot_sessions session
  ON session.team_id = bot.team_id
 AND session.bot_id = bot.id
 AND session.id = sqlc.narg(session_id)
 AND session.deleted_at IS NULL
WHERE bot.team_id = public.memoh_current_team_id()
  AND bot.id = sqlc.arg(bot_id)
  AND (
    sqlc.narg(session_id)::uuid IS NULL
    OR session.id IS NOT NULL
  );

-- name: GetRuntimeRoundOutcome :one
-- Read the terminal runtime outcome for commit-unknown reconciliation after
-- the caller has first waited on the session write lock.
SELECT COALESCE(message.metadata->>'agent_turn_outcome', '')::text AS outcome
FROM bot_history_messages message
JOIN session_runs run
  ON run.team_id = message.team_id
 AND run.run_id = message.run_id
 AND run.bot_id = message.bot_id
 AND run.session_id = message.session_id
 AND run.turn_id = message.turn_id
 AND run.turn_position = message.turn_position
WHERE message.team_id = public.memoh_current_team_id()
  AND message.bot_id = sqlc.arg(bot_id)
  AND message.session_id = sqlc.arg(session_id)
  AND message.run_id = sqlc.arg(run_id)
  AND message.role = 'assistant'
  AND message.runtime_type <> 'model'
  AND message.turn_visible = true
  AND message.metadata->>'agent_turn_outcome' IN ('succeeded', 'failed', 'aborted')
ORDER BY message.turn_message_seq DESC, message.created_at DESC, message.id DESC
LIMIT 1;

-- name: GetRuntimeLeadingUserMessageID :one
-- Reconcile the single eager user row after an uncertain COMMIT. The caller
-- first waits on the session row in a separate statement, so no row here is a
-- definitive rollback rather than an observation racing the old backend.
SELECT message.id
FROM bot_history_messages message
JOIN session_runs run
  ON run.team_id = message.team_id
 AND run.run_id = message.run_id
 AND run.bot_id = message.bot_id
 AND run.session_id = message.session_id
 AND run.turn_id = message.turn_id
 AND run.turn_position = message.turn_position
WHERE message.team_id = public.memoh_current_team_id()
  AND message.bot_id = sqlc.arg(bot_id)
  AND message.session_id = sqlc.arg(session_id)
  AND message.run_id = sqlc.arg(run_id)
  AND message.turn_id = sqlc.arg(turn_id)
  AND message.role = 'user'
  AND message.runtime_type <> 'model'
  AND message.turn_message_seq = 1
ORDER BY message.created_at DESC, message.id DESC
LIMIT 1;

-- name: DeleteRuntimeDecisionProjectionsByRun :execrows
-- Decision cards are temporary stream projections. Delete by their stable
-- run marker as well as by returned IDs so an insert whose COMMIT ack was lost
-- cannot survive the final canonical round as a duplicate assistant message.
-- Deleting context rows rewinds the session, locked before its rows as every
-- history mutation locks them, to the newest checkpoint that does not depend
-- on them.
WITH owner_session AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = sqlc.arg(session_id)
    AND session.bot_id = sqlc.arg(bot_id)
  FOR UPDATE
), projections AS MATERIALIZED (
  SELECT message.id, message.turn_position, message.turn_message_seq,
    (
      message.turn_visible
      AND message.turn_id IS NOT NULL
      AND message.turn_position IS NOT NULL
      AND message.turn_message_seq IS NOT NULL
    ) AS in_context
  FROM bot_history_messages message
  JOIN owner_session owner ON owner.id = message.session_id
  WHERE message.team_id = public.memoh_current_team_id()
    AND message.bot_id = sqlc.arg(bot_id)
    AND message.run_id = sqlc.arg(run_id)
    AND message.role = 'assistant'
    AND message.runtime_type <> 'model'
    AND message.metadata @> '{"agent_decision_projection": true}'::jsonb
  FOR UPDATE OF message
), rewound AS (
  SELECT public.memoh_rewind_history_checkpoint(owner.id, removed.turn_position, removed.turn_message_seq) AS done
  FROM owner_session owner
  CROSS JOIN (
    SELECT
      (array_agg(projection.turn_position ORDER BY projection.turn_position, projection.turn_message_seq))[1]::bigint AS turn_position,
      (array_agg(projection.turn_message_seq ORDER BY projection.turn_position, projection.turn_message_seq))[1]::bigint AS turn_message_seq
    FROM projections projection
    WHERE projection.in_context
  ) removed
  WHERE removed.turn_position IS NOT NULL
)
DELETE FROM bot_history_messages message
USING projections
WHERE message.team_id = public.memoh_current_team_id()
  AND message.id = projections.id
  AND (SELECT count(*) FROM rewound) >= 0;
