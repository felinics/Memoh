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

-- name: GetAgentSessionPublicationHead :one
-- Return the last committed round. Warm runtimes check it before every native
-- prompt so another server instance (or a history clear) cannot leave two
-- process-local conversations advancing from different canonical heads.
SELECT publication.run_id
FROM agent_session_publications publication
JOIN bot_sessions session
  ON session.team_id = publication.team_id
 AND session.id = publication.session_id
WHERE publication.team_id = public.memoh_current_team_id()
  AND publication.session_id = sqlc.arg(session_id)
  AND session.bot_id = sqlc.arg(bot_id)
  AND session.runtime_type <> 'model'
  AND session.deleted_at IS NULL;

-- name: UpsertAgentSessionPublication :execrows
-- Written in the same transaction as the round's canonical messages. The run
-- join proves the run belongs to this tenant+session and still owns the
-- session's current fencing generation, so a stale runtime cannot move the
-- head.
WITH target_session AS MATERIALIZED (
  SELECT session.team_id, session.id, session.runtime_fencing_token
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = sqlc.arg(session_id)
    AND session.bot_id = sqlc.arg(bot_id)
    AND session.runtime_type <> 'model'
    AND session.deleted_at IS NULL
),
target_run AS MATERIALIZED (
  SELECT target.team_id, target.id AS session_id, run.run_id
  FROM target_session target
  JOIN session_runs run
    ON run.team_id = target.team_id
   AND run.session_id = target.id
   AND run.bot_id = sqlc.arg(bot_id)
   AND run.run_id = sqlc.arg(run_id)
   AND run.fencing_token = target.runtime_fencing_token
)
INSERT INTO agent_session_publications (team_id, session_id, run_id)
SELECT target.team_id, target.session_id, target.run_id
FROM target_run target
ON CONFLICT (team_id, session_id) DO UPDATE SET
  run_id = EXCLUDED.run_id,
  updated_at = now();

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
DELETE FROM bot_history_messages message
WHERE message.team_id = public.memoh_current_team_id()
  AND message.bot_id = sqlc.arg(bot_id)
  AND message.session_id = sqlc.arg(session_id)
  AND message.run_id = sqlc.arg(run_id)
  AND message.role = 'assistant'
  AND message.runtime_type <> 'model'
  AND message.metadata @> '{"agent_decision_projection": true}'::jsonb;

-- name: DeleteAgentSessionPublicationsBySession :execrows
DELETE FROM agent_session_publications publication
WHERE publication.team_id = public.memoh_current_team_id()
  AND publication.session_id = sqlc.arg(session_id);
