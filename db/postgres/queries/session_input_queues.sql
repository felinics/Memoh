-- name: ListPendingSteerQueue :many
SELECT * FROM session_steer_queue WHERE team_id = public.memoh_current_team_id() AND session_id = sqlc.arg(session_id) AND status = 'accepted' ORDER BY position, item_id;
-- name: ListPendingSessionQueues :many
-- One round trip for the UI list endpoint. Both queues are read in the same
-- statement snapshot and bounded per queue; the queue column tells the caller
-- which typed row shape to decode. Steer rows carry target_run_id in run_id,
-- follow-up rows carry enqueued_during_run_id.
WITH steer AS (
  SELECT 'steer'::text AS queue, q.item_id, q.bot_id, q.session_id, q.target_run_id AS run_id, q.payload, q.status, q.position, q.created_at
  FROM session_steer_queue q
  WHERE q.team_id = public.memoh_current_team_id() AND q.session_id = sqlc.arg(target_session_id)::uuid AND q.status = 'accepted'
  ORDER BY q.position, q.item_id
  LIMIT sqlc.arg(max_items)::bigint
), follow_up AS (
  SELECT 'follow_up'::text AS queue, q.item_id, q.bot_id, q.session_id, q.enqueued_during_run_id AS run_id, q.payload, q.status, q.position, q.created_at
  FROM session_follow_up_queue q
  WHERE q.team_id = public.memoh_current_team_id() AND q.session_id = sqlc.arg(target_session_id)::uuid AND q.status = 'accepted' AND q.assigned_run_id IS NULL
  ORDER BY q.position, q.item_id
  LIMIT sqlc.arg(max_items)::bigint
)
SELECT * FROM steer
UNION ALL
SELECT * FROM follow_up;
-- name: GetSteerQueueItem :one
SELECT * FROM session_steer_queue WHERE team_id=public.memoh_current_team_id() AND item_id=sqlc.arg(item_id);
-- name: LockSessionForQueueAdmission :one
SELECT id FROM bot_sessions WHERE team_id=public.memoh_current_team_id() AND id=sqlc.arg(session_id) AND bot_id=sqlc.arg(bot_id) AND deleted_at IS NULL FOR UPDATE;
-- name: EnqueueSteerQueueItem :one
-- Admission locks the session row so concurrent enqueues serialize and take
-- contiguous positions. The active run is re-read under that lock. A replayed
-- invocation returns no row (ON CONFLICT DO NOTHING); callers look it up.
WITH locked_session AS MATERIALIZED (
  SELECT s.id
  FROM bot_sessions s
  JOIN bots bot ON bot.team_id=s.team_id AND bot.id=s.bot_id
  WHERE s.team_id=public.memoh_current_team_id() AND s.id=sqlc.arg(session_id)
    AND s.bot_id=sqlc.arg(bot_id) AND s.deleted_at IS NULL AND bot.status <> 'deleting'
  FOR UPDATE OF s
), active_run AS MATERIALIZED (
  SELECT run.run_id
  FROM session_runs run
  JOIN locked_session s ON s.id=run.session_id
  WHERE run.team_id=public.memoh_current_team_id() AND run.bot_id=sqlc.arg(bot_id)
    AND run.state IN ('accepted','running','waiting_decision')
  LIMIT 1
), base AS MATERIALIZED (
  SELECT COALESCE(max(position), 0) + 1 AS position
  FROM session_steer_queue
  WHERE team_id=public.memoh_current_team_id() AND session_id=sqlc.arg(session_id)
)
INSERT INTO session_steer_queue (item_id, team_id, bot_id, session_id, target_run_id, invocation_id, payload, position)
SELECT sqlc.arg(item_id), public.memoh_current_team_id(), sqlc.arg(bot_id), sqlc.arg(session_id), run.run_id, sqlc.arg(invocation_id), sqlc.arg(payload)::jsonb, base.position
FROM active_run run
CROSS JOIN base
ON CONFLICT (team_id, session_id, invocation_id) DO NOTHING
RETURNING *;
-- name: GetSteerQueueItemByInvocation :one
SELECT * FROM session_steer_queue
WHERE team_id=public.memoh_current_team_id() AND session_id=sqlc.arg(session_id) AND invocation_id=sqlc.arg(invocation_id);
-- name: EnqueueFollowUpQueueItem :one
WITH locked_session AS MATERIALIZED (
  SELECT s.id
  FROM bot_sessions s
  JOIN bots bot ON bot.team_id=s.team_id AND bot.id=s.bot_id
  WHERE s.team_id=public.memoh_current_team_id() AND s.id=sqlc.arg(session_id)
    AND s.bot_id=sqlc.arg(bot_id) AND s.deleted_at IS NULL AND bot.status <> 'deleting'
  FOR UPDATE OF s
), active_run AS MATERIALIZED (
  SELECT run.run_id
  FROM session_runs run
  JOIN locked_session s ON s.id=run.session_id
  WHERE run.team_id=public.memoh_current_team_id() AND run.bot_id=sqlc.arg(bot_id)
    AND run.state IN ('accepted','running','waiting_decision')
  LIMIT 1
), base AS MATERIALIZED (
  SELECT COALESCE(max(position), 0) + 1 AS position
  FROM session_follow_up_queue
  WHERE team_id=public.memoh_current_team_id() AND session_id=sqlc.arg(session_id)
)
INSERT INTO session_follow_up_queue (item_id, team_id, bot_id, session_id, enqueued_during_run_id, invocation_id, payload, position)
SELECT sqlc.arg(item_id), public.memoh_current_team_id(), sqlc.arg(bot_id), sqlc.arg(session_id), run.run_id, sqlc.arg(invocation_id), sqlc.arg(payload)::jsonb, base.position
FROM active_run run
CROSS JOIN base
ON CONFLICT (team_id, session_id, invocation_id) DO NOTHING
RETURNING *;
-- name: GetFollowUpQueueItemByInvocation :one
SELECT * FROM session_follow_up_queue
WHERE team_id=public.memoh_current_team_id() AND session_id=sqlc.arg(session_id) AND invocation_id=sqlc.arg(invocation_id);
-- name: ListPendingFollowUpQueue :many
SELECT * FROM session_follow_up_queue WHERE team_id = public.memoh_current_team_id() AND session_id = sqlc.arg(session_id) AND status = 'accepted' AND assigned_run_id IS NULL ORDER BY position, item_id;
-- name: GetNextPendingFollowUpQueueItem :one
SELECT * FROM session_follow_up_queue
WHERE team_id = public.memoh_current_team_id()
  AND session_id = sqlc.arg(session_id)
  AND status = 'accepted'
  AND assigned_run_id IS NULL
ORDER BY position, item_id
LIMIT 1;
-- name: GetFollowUpQueueItem :one
SELECT * FROM session_follow_up_queue WHERE team_id=public.memoh_current_team_id() AND item_id=sqlc.arg(item_id);
-- name: UpdateAcceptedSteerQueueItemPayload :one
UPDATE session_steer_queue SET payload=sqlc.arg(payload), updated_at=now()
WHERE team_id=public.memoh_current_team_id() AND session_id=sqlc.arg(session_id)
  AND item_id=sqlc.arg(item_id) AND status='accepted'
RETURNING *;
-- name: UpdateAcceptedFollowUpQueueItemPayload :one
UPDATE session_follow_up_queue SET payload=sqlc.arg(payload), updated_at=now()
WHERE team_id=public.memoh_current_team_id() AND session_id=sqlc.arg(session_id)
  AND item_id=sqlc.arg(item_id) AND status='accepted' AND assigned_run_id IS NULL
RETURNING *;
-- name: CancelAcceptedSteerQueueItem :one
UPDATE session_steer_queue SET status='canceled', updated_at=now()
WHERE team_id=public.memoh_current_team_id() AND session_id=sqlc.arg(session_id)
  AND item_id=sqlc.arg(item_id) AND status='accepted'
RETURNING *;
-- name: CancelAcceptedFollowUpQueueItem :one
UPDATE session_follow_up_queue SET status='canceled', updated_at=now()
WHERE team_id=public.memoh_current_team_id() AND session_id=sqlc.arg(session_id)
  AND item_id=sqlc.arg(item_id) AND status='accepted' AND assigned_run_id IS NULL
RETURNING *;
-- name: ReorderAcceptedSteerQueue :many
WITH locked_session AS MATERIALIZED (
  SELECT id
  FROM bot_sessions
  WHERE team_id = public.memoh_current_team_id()
    AND id = sqlc.arg(session_id)
    AND deleted_at IS NULL
  FOR UPDATE
), item AS MATERIALIZED (
  SELECT q.item_id, q.position
  FROM session_steer_queue q
  JOIN locked_session s ON s.id = q.session_id
  WHERE q.team_id = public.memoh_current_team_id()
    AND q.session_id = sqlc.arg(session_id)
    AND q.item_id = sqlc.arg(item_id)
    AND q.status = 'accepted'
), before_item AS MATERIALIZED (
  SELECT q.item_id, q.position
  FROM session_steer_queue q
  JOIN locked_session s ON s.id = q.session_id
  WHERE q.team_id = public.memoh_current_team_id()
    AND q.session_id = sqlc.arg(session_id)
    AND q.item_id = sqlc.narg(before_item_id)::uuid
    AND q.status = 'accepted'
), tail AS MATERIALIZED (
  SELECT COALESCE(MAX(q.position), 0) + 1 AS position
  FROM session_steer_queue q
  JOIN locked_session s ON s.id = q.session_id
  WHERE q.team_id = public.memoh_current_team_id()
    AND q.session_id = sqlc.arg(session_id)
    AND q.status = 'accepted'
), target AS MATERIALIZED (
  SELECT i.item_id, i.position AS current_position,
    CASE WHEN sqlc.narg(before_item_id)::uuid IS NULL THEN tail.position ELSE b.position END AS target_position
  FROM item i
  CROSS JOIN tail
  LEFT JOIN before_item b ON TRUE
  WHERE sqlc.narg(before_item_id)::uuid IS NULL OR b.item_id IS NOT NULL
), moved AS (
  UPDATE session_steer_queue q
  SET position = CASE
    WHEN q.item_id = target.item_id THEN target.target_position
    WHEN target.target_position < target.current_position
      AND q.position >= target.target_position AND q.position < target.current_position THEN q.position + 1
    WHEN target.target_position > target.current_position
      AND q.position > target.current_position AND q.position <= target.target_position THEN q.position - 1
    ELSE q.position
  END,
  updated_at = now()
  FROM target
  WHERE q.team_id = public.memoh_current_team_id()
    AND q.session_id = sqlc.arg(session_id)
    AND q.status = 'accepted'
    AND (
      q.item_id = target.item_id
      OR target.target_position < target.current_position
        AND q.position >= target.target_position AND q.position < target.current_position
      OR target.target_position > target.current_position
        AND q.position > target.current_position AND q.position <= target.target_position
    )
  RETURNING q.*
)
SELECT * FROM moved
UNION ALL
SELECT q.*
FROM session_steer_queue q
JOIN locked_session s ON s.id = q.session_id
WHERE q.team_id = public.memoh_current_team_id()
  AND q.session_id = sqlc.arg(session_id)
  AND q.status = 'accepted'
  AND EXISTS (SELECT 1 FROM target)
  AND NOT EXISTS (SELECT 1 FROM moved m WHERE m.item_id = q.item_id)
ORDER BY position, item_id;
-- name: ReorderAcceptedFollowUpQueue :many
WITH locked_session AS MATERIALIZED (
  SELECT id
  FROM bot_sessions
  WHERE team_id = public.memoh_current_team_id()
    AND id = sqlc.arg(session_id)
    AND deleted_at IS NULL
  FOR UPDATE
), item AS MATERIALIZED (
  SELECT q.item_id, q.position
  FROM session_follow_up_queue q
  JOIN locked_session s ON s.id = q.session_id
  WHERE q.team_id = public.memoh_current_team_id()
    AND q.session_id = sqlc.arg(session_id)
    AND q.item_id = sqlc.arg(item_id)
    AND q.status = 'accepted'
    AND q.assigned_run_id IS NULL
), before_item AS MATERIALIZED (
  SELECT q.item_id, q.position
  FROM session_follow_up_queue q
  JOIN locked_session s ON s.id = q.session_id
  WHERE q.team_id = public.memoh_current_team_id()
    AND q.session_id = sqlc.arg(session_id)
    AND q.item_id = sqlc.narg(before_item_id)::uuid
    AND q.status = 'accepted'
    AND q.assigned_run_id IS NULL
), tail AS MATERIALIZED (
  SELECT COALESCE(MAX(q.position), 0) + 1 AS position
  FROM session_follow_up_queue q
  JOIN locked_session s ON s.id = q.session_id
  WHERE q.team_id = public.memoh_current_team_id()
    AND q.session_id = sqlc.arg(session_id)
    AND q.status = 'accepted'
    AND q.assigned_run_id IS NULL
), target AS MATERIALIZED (
  SELECT i.item_id, i.position AS current_position,
    CASE WHEN sqlc.narg(before_item_id)::uuid IS NULL THEN tail.position ELSE b.position END AS target_position
  FROM item i
  CROSS JOIN tail
  LEFT JOIN before_item b ON TRUE
  WHERE sqlc.narg(before_item_id)::uuid IS NULL OR b.item_id IS NOT NULL
), moved AS (
  UPDATE session_follow_up_queue q
  SET position = CASE
    WHEN q.item_id = target.item_id THEN target.target_position
    WHEN target.target_position < target.current_position
      AND q.position >= target.target_position AND q.position < target.current_position THEN q.position + 1
    WHEN target.target_position > target.current_position
      AND q.position > target.current_position AND q.position <= target.target_position THEN q.position - 1
    ELSE q.position
  END,
  updated_at = now()
  FROM target
  WHERE q.team_id = public.memoh_current_team_id()
    AND q.session_id = sqlc.arg(session_id)
    AND q.status = 'accepted'
    AND q.assigned_run_id IS NULL
    AND (
      q.item_id = target.item_id
      OR target.target_position < target.current_position
        AND q.position >= target.target_position AND q.position < target.current_position
      OR target.target_position > target.current_position
        AND q.position > target.current_position AND q.position <= target.target_position
    )
  RETURNING q.*
)
SELECT * FROM moved
UNION ALL
SELECT q.*
FROM session_follow_up_queue q
JOIN locked_session s ON s.id = q.session_id
WHERE q.team_id = public.memoh_current_team_id()
  AND q.session_id = sqlc.arg(session_id)
  AND q.status = 'accepted'
  AND q.assigned_run_id IS NULL
  AND EXISTS (SELECT 1 FROM target)
  AND NOT EXISTS (SELECT 1 FROM moved m WHERE m.item_id = q.item_id)
ORDER BY position, item_id;
-- name: ClaimNextSteerQueueItem :one
WITH candidate AS MATERIALIZED (
  SELECT q.item_id
  FROM session_steer_queue AS q
  WHERE q.team_id=public.memoh_current_team_id()
    AND q.session_id=sqlc.arg(session_id)
    AND q.target_run_id=sqlc.arg(execution_run_id)
    AND q.status='accepted'
  ORDER BY q.position, q.item_id
  LIMIT 1
  FOR UPDATE
)
UPDATE session_steer_queue AS target
SET status='claimed', claim_run_id=sqlc.arg(execution_run_id), claim_owner_id=sqlc.arg(execution_owner_id), claim_fencing_token=sqlc.arg(execution_fencing_token), updated_at=now()
FROM candidate
WHERE target.team_id=public.memoh_current_team_id()
  AND target.item_id=candidate.item_id
  AND EXISTS (SELECT 1 FROM session_runs run WHERE run.team_id=public.memoh_current_team_id() AND run.run_id=sqlc.arg(execution_run_id) AND run.owner_id=sqlc.arg(execution_owner_id) AND run.fencing_token=sqlc.arg(execution_fencing_token) AND run.state IN ('accepted','running','waiting_decision'))
  AND NOT EXISTS (SELECT 1 FROM session_steer_queue claimed WHERE claimed.team_id=public.memoh_current_team_id() AND claimed.claim_run_id=sqlc.arg(execution_run_id) AND claimed.status='claimed')
  AND NOT EXISTS (SELECT 1 FROM session_follow_up_queue claimed WHERE claimed.team_id=public.memoh_current_team_id() AND claimed.claim_run_id=sqlc.arg(execution_run_id) AND claimed.status='claimed')
RETURNING target.*;
-- name: ClaimAssignedFollowUpQueueItem :one
UPDATE session_follow_up_queue AS target SET status='claimed', claim_run_id=sqlc.arg(execution_run_id), claim_owner_id=sqlc.arg(execution_owner_id), claim_fencing_token=sqlc.arg(execution_fencing_token), updated_at=now()
WHERE target.team_id=public.memoh_current_team_id() AND target.item_id=sqlc.arg(queue_item_id) AND target.assigned_run_id=sqlc.arg(execution_run_id) AND target.status='accepted'
  AND EXISTS (SELECT 1 FROM session_runs run WHERE run.team_id=public.memoh_current_team_id() AND run.run_id=sqlc.arg(execution_run_id) AND run.owner_id=sqlc.arg(execution_owner_id) AND run.fencing_token=sqlc.arg(execution_fencing_token) AND run.state IN ('accepted','running','waiting_decision'))
  AND NOT EXISTS (SELECT 1 FROM session_steer_queue claimed WHERE claimed.team_id=public.memoh_current_team_id() AND claimed.claim_run_id=sqlc.arg(execution_run_id) AND claimed.status='claimed')
  AND NOT EXISTS (SELECT 1 FROM session_follow_up_queue claimed WHERE claimed.team_id=public.memoh_current_team_id() AND claimed.claim_run_id=sqlc.arg(execution_run_id) AND claimed.status='claimed')
RETURNING *;
-- name: ReclaimSteerQueueItem :one
UPDATE session_steer_queue AS target SET claim_owner_id=sqlc.arg(execution_owner_id), claim_fencing_token=sqlc.arg(execution_fencing_token), updated_at=now()
WHERE target.team_id=public.memoh_current_team_id() AND target.item_id=sqlc.arg(queue_item_id) AND target.target_run_id=sqlc.arg(execution_run_id) AND target.claim_run_id=sqlc.arg(execution_run_id) AND target.status='claimed'
  AND EXISTS (SELECT 1 FROM session_runs run WHERE run.team_id=public.memoh_current_team_id() AND run.run_id=sqlc.arg(execution_run_id) AND run.owner_id=sqlc.arg(execution_owner_id) AND run.fencing_token=sqlc.arg(execution_fencing_token) AND run.state IN ('accepted','running','waiting_decision'))
RETURNING *;
-- name: ApplySteerQueueItem :one
UPDATE session_steer_queue AS target SET status='applied', claim_run_id=NULL, claim_owner_id=NULL, claim_fencing_token=NULL, updated_at=now()
WHERE target.team_id=public.memoh_current_team_id() AND target.item_id=sqlc.arg(queue_item_id) AND target.claim_run_id=sqlc.arg(execution_run_id) AND target.claim_owner_id=sqlc.arg(execution_owner_id) AND target.claim_fencing_token=sqlc.arg(execution_fencing_token) AND target.status='claimed'
  AND EXISTS (SELECT 1 FROM session_runs run WHERE run.team_id=public.memoh_current_team_id() AND run.run_id=sqlc.arg(execution_run_id) AND run.owner_id=sqlc.arg(execution_owner_id) AND run.fencing_token=sqlc.arg(execution_fencing_token) AND run.state IN ('accepted','running','waiting_decision'))
RETURNING *;
-- name: ApplyFollowUpQueueItem :one
UPDATE session_follow_up_queue AS target SET status='applied', claim_run_id=NULL, claim_owner_id=NULL, claim_fencing_token=NULL, updated_at=now()
WHERE target.team_id=public.memoh_current_team_id() AND target.item_id=sqlc.arg(queue_item_id) AND target.claim_run_id=sqlc.arg(execution_run_id) AND target.claim_owner_id=sqlc.arg(execution_owner_id) AND target.claim_fencing_token=sqlc.arg(execution_fencing_token) AND target.status='claimed'
  AND EXISTS (SELECT 1 FROM session_runs run WHERE run.team_id=public.memoh_current_team_id() AND run.run_id=sqlc.arg(execution_run_id) AND run.owner_id=sqlc.arg(execution_owner_id) AND run.fencing_token=sqlc.arg(execution_fencing_token) AND run.state IN ('accepted','running','waiting_decision'))
RETURNING *;
-- name: RejectSteerItemsForRun :exec
UPDATE session_steer_queue SET status='rejected', claim_run_id=NULL, claim_owner_id=NULL, claim_fencing_token=NULL, updated_at=now() WHERE team_id=public.memoh_current_team_id() AND target_run_id=sqlc.arg(run_id) AND status IN ('accepted','claimed');
-- name: RejectFollowUpForContinuation :exec
UPDATE session_follow_up_queue SET status='rejected', claim_run_id=NULL, claim_owner_id=NULL, claim_fencing_token=NULL, updated_at=now() WHERE team_id=public.memoh_current_team_id() AND assigned_run_id=sqlc.arg(run_id) AND status IN ('accepted','claimed');
-- name: RejectUnassignedFollowUpsForRun :exec
UPDATE session_follow_up_queue SET status='rejected', claim_run_id=NULL, claim_owner_id=NULL, claim_fencing_token=NULL, updated_at=now()
WHERE team_id=public.memoh_current_team_id() AND enqueued_during_run_id=sqlc.arg(run_id) AND assigned_run_id IS NULL AND status IN ('accepted','claimed');
-- name: RejectUnassignedFollowUpsForSession :exec
UPDATE session_follow_up_queue SET status='rejected', claim_run_id=NULL, claim_owner_id=NULL, claim_fencing_token=NULL, updated_at=now()
WHERE team_id=public.memoh_current_team_id() AND session_id=sqlc.arg(session_id) AND assigned_run_id IS NULL AND status IN ('accepted','claimed');
-- name: ListOwnerlessContinuationRuns :many
SELECT * FROM session_runs WHERE team_id=public.memoh_current_team_id() AND state='accepted' AND owner_id IS NULL AND source_follow_up_item_id IS NOT NULL ORDER BY created_at,run_id LIMIT sqlc.arg(batch_size);
-- name: GetClaimedSteerQueueItemForRun :one
SELECT * FROM session_steer_queue
WHERE team_id=public.memoh_current_team_id() AND claim_run_id=sqlc.arg(run_id)
  AND status='claimed'
  AND EXISTS (
    SELECT 1 FROM session_queue_step_commits commit
    WHERE commit.team_id=public.memoh_current_team_id()
      AND commit.run_id=sqlc.arg(run_id)
      AND commit.steer_item_id=session_steer_queue.item_id
      AND commit.action='continue_with_steer'
  )
ORDER BY created_at, item_id LIMIT 1;
-- name: AcquireQueuedRun :one
UPDATE session_runs
SET owner_id=sqlc.arg(execution_owner_id), owner_since=now(),
    fencing_token=nextval('session_runtime_fencing_token_seq'),
    live_generation=sqlc.arg(live_generation), updated_at=now()
WHERE team_id=public.memoh_current_team_id() AND run_id=sqlc.arg(run_id)
  AND state='running' AND owner_id IS NOT NULL
  AND fencing_token=sqlc.arg(previous_fencing_token)
RETURNING *;
-- name: AcquireContinuationRun :one
WITH candidate AS MATERIALIZED (
  SELECT run.run_id
  FROM session_runs run
  JOIN bot_sessions session ON session.team_id=run.team_id AND session.id=run.session_id
  JOIN bots bot ON bot.team_id=run.team_id AND bot.id=run.bot_id
  WHERE run.team_id=public.memoh_current_team_id() AND run.run_id=sqlc.arg(continuation_run_id)
    AND run.state='accepted' AND run.owner_id IS NULL AND run.source_follow_up_item_id IS NOT NULL
    AND session.deleted_at IS NULL AND bot.status <> 'deleting'
    AND (bot.runtime_reset_expires_at IS NULL OR bot.runtime_reset_expires_at <= clock_timestamp())
    AND (session.runtime_reset_expires_at IS NULL OR session.runtime_reset_expires_at <= clock_timestamp())
  FOR UPDATE OF run
)
UPDATE session_runs run
SET owner_id=sqlc.arg(execution_owner_id), owner_since=now(), fencing_token=nextval('session_runtime_fencing_token_seq'),
    live_generation=sqlc.arg(live_generation), state='running', updated_at=now()
FROM candidate
WHERE run.team_id=public.memoh_current_team_id() AND run.run_id=candidate.run_id
RETURNING run.*;
-- name: CreateContinuationFromFollowUp :one
-- A follow-up's enqueued_during_run_id is admission provenance, not a parent
-- constraint. The next completed continuation may hand off the next accepted
-- item from the same session, preserving FIFO across R1, R2, and later runs.
WITH parent AS MATERIALIZED (
  SELECT run.run_id, run.team_id, run.bot_id, run.session_id
  FROM session_runs run
  WHERE run.team_id=public.memoh_current_team_id() AND run.run_id=sqlc.arg(parent_run_id)
    AND run.fencing_token=sqlc.arg(parent_fencing_token) AND run.state='completed'
), picked AS MATERIALIZED (
  SELECT f.item_id, f.team_id, f.bot_id, f.session_id, f.payload, f.assigned_run_id
  FROM session_follow_up_queue f
  JOIN parent ON parent.team_id=f.team_id AND parent.bot_id=f.bot_id AND parent.session_id=f.session_id
  WHERE f.team_id=public.memoh_current_team_id() AND f.item_id=sqlc.arg(item_id) AND f.status='accepted'
    AND (f.assigned_run_id IS NULL OR f.assigned_run_id=sqlc.arg(run_id))
  FOR UPDATE
), existing AS MATERIALIZED (
  SELECT run.*
  FROM session_runs run JOIN picked p ON p.team_id=run.team_id AND p.item_id=run.source_follow_up_item_id
  WHERE run.run_id=sqlc.arg(run_id)
), position AS (
  UPDATE bot_sessions s SET next_turn_position=s.next_turn_position+1
  FROM picked p WHERE s.team_id=p.team_id AND s.id=p.session_id AND p.assigned_run_id IS NULL AND NOT EXISTS (SELECT 1 FROM existing)
  RETURNING s.next_turn_position-1 AS turn_position
), assigned AS (
  UPDATE session_follow_up_queue f SET assigned_run_id=sqlc.arg(run_id), updated_at=now()
  FROM picked p WHERE f.team_id=p.team_id AND f.item_id=p.item_id AND f.assigned_run_id IS NULL
  RETURNING f.item_id
), inserted AS (
INSERT INTO session_runs (run_id, team_id, bot_id, session_id, invocation_id, turn_id, turn_position, state, input_json, input_fingerprint, source_follow_up_item_id)
SELECT sqlc.arg(run_id), p.team_id, p.bot_id, p.session_id, sqlc.arg(invocation_id), sqlc.arg(turn_id), pos.turn_position, 'accepted', p.payload, sqlc.arg(input_fingerprint), p.item_id
FROM picked p CROSS JOIN position pos JOIN assigned a ON a.item_id=p.item_id
ON CONFLICT (team_id, run_id) DO NOTHING
RETURNING *
)
SELECT * FROM inserted
UNION ALL
SELECT * FROM existing
LIMIT 1;

-- name: GetSessionQueueStepCommit :one
SELECT * FROM session_queue_step_commits
WHERE team_id=public.memoh_current_team_id() AND run_id=sqlc.arg(run_id) AND step_index=sqlc.arg(step_index);
-- name: NextSessionQueueStepIndex :one
SELECT COALESCE(MAX(step_index) + 1, 0)::BIGINT AS next_step_index
FROM session_queue_step_commits
WHERE team_id=public.memoh_current_team_id() AND run_id=sqlc.arg(run_id);

-- name: CreateSessionQueueStepCommit :one
INSERT INTO session_queue_step_commits (
  team_id, run_id, step_index, commit_hash, action,
  steer_item_id, follow_up_item_id, continuation_run_id
) VALUES (
  public.memoh_current_team_id(), sqlc.arg(run_id), sqlc.arg(step_index), sqlc.arg(commit_hash), sqlc.arg(action),
  sqlc.narg(steer_item_id), sqlc.narg(follow_up_item_id), sqlc.narg(continuation_run_id)
)
ON CONFLICT (team_id, run_id, step_index) DO NOTHING
RETURNING *;
