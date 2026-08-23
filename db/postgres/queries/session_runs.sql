-- name: AdmitLockedSessionRun :one
-- Durable admission. There is no queue: SR-OWN-001 permits answering busy, so a
-- second concurrent invocation is rejected rather than parked.
--
-- Busy is rejected in two layers. The adapter's indexed pre-read catches the
-- common case before this statement runs at all, so contended submissions never
-- reach the write path. This statement handles only what slipped through that
-- unlocked read: session_runs_single_active raises, and because the adapter
-- runs this after a separate parent lock in one transaction, rollback takes the
-- allocated turn position with it and leaves nothing for the caller to clean up.
--
-- Returns no rows when this invocation_id was already admitted — ON CONFLICT
-- keeps the ordinary retry path free of errors, which matters because channel
-- redelivery makes it a routine event — or when the session does not exist. The
-- caller re-selects by invocation to tell those two apart.
--
-- A position is allocated only for an admission that can still succeed: never
-- for a known duplicate (the NOT EXISTS guard below) and never for a rejected
-- one (the rollback). The single exception is two callers racing on the *same*
-- invocation_id, where both pass the guard against the statement snapshot and
-- the loser's increment commits with no row. That leaves a gap in turn_position,
-- which is ordering-only and never read as a count.
WITH target_session AS MATERIALIZED (
  SELECT s.id AS session_id
  FROM bot_sessions AS s
  JOIN bots bot ON bot.team_id = s.team_id AND bot.id = s.bot_id
  WHERE s.team_id = public.memoh_current_team_id()
    AND s.id = sqlc.arg(session_id)
    AND s.bot_id = sqlc.arg(bot_id)
    AND s.deleted_at IS NULL
    AND bot.status <> 'deleting'
    AND (
      bot.runtime_reset_expires_at IS NULL
      OR bot.runtime_reset_expires_at <= clock_timestamp()
    )
    AND (
      s.runtime_reset_expires_at IS NULL
      OR s.runtime_reset_expires_at <= clock_timestamp()
    )
),
existing AS (
  SELECT 1 AS present
  FROM session_runs AS prior
  WHERE prior.team_id = public.memoh_current_team_id()
    AND prior.session_id = sqlc.arg(session_id)
    AND prior.invocation_id = sqlc.arg(invocation_id)
),
allocated_position AS (
  UPDATE bot_sessions AS s
  SET next_turn_position = s.next_turn_position + 1
  FROM target_session
  WHERE s.team_id = public.memoh_current_team_id()
    AND s.id = target_session.session_id
    AND NOT EXISTS (SELECT 1 FROM existing)
  RETURNING (s.next_turn_position - 1)::bigint AS turn_position
)
INSERT INTO session_runs (
  run_id,
  bot_id,
  session_id,
  invocation_id,
  turn_id,
  turn_position,
  state,
  input_json,
  input_fingerprint
)
SELECT
  sqlc.arg(run_id),
  sqlc.arg(bot_id),
  target_session.session_id,
  sqlc.arg(invocation_id),
  sqlc.arg(turn_id),
  allocated_position.turn_position,
  'accepted',
  sqlc.arg(input_json),
  sqlc.arg(input_fingerprint)
FROM target_session
CROSS JOIN allocated_position
ON CONFLICT (team_id, session_id, invocation_id) DO NOTHING
RETURNING *;

-- name: GetSessionRun :one
SELECT *
FROM session_runs
WHERE team_id = public.memoh_current_team_id()
  AND run_id = sqlc.arg(run_id);

-- name: GetSessionRunByInvocation :one
SELECT *
FROM session_runs
WHERE team_id = public.memoh_current_team_id()
  AND session_id = sqlc.arg(session_id)
  AND invocation_id = sqlc.arg(invocation_id);

-- name: GetActiveSessionRun :one
SELECT *
FROM session_runs
WHERE team_id = public.memoh_current_team_id()
  AND session_id = sqlc.arg(session_id)
  AND state IN ('accepted', 'running', 'waiting_decision');

-- name: ListActiveSessionRunsByBot :many
SELECT *
FROM session_runs
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND state IN ('accepted', 'running', 'waiting_decision')
ORDER BY session_id, run_id;

-- name: GetLatestSessionRun :one
SELECT *
FROM session_runs
WHERE team_id = public.memoh_current_team_id()
  AND session_id = sqlc.arg(session_id)
ORDER BY created_at DESC, run_id DESC
LIMIT 1;

-- name: LockBotForSessionRunClaim :one
-- Claim uses a real transaction and a fresh statement after this parent lock.
-- Reset acquisition takes the same lock, so neither can cross the other's
-- committed gate using a statement snapshot captured while waiting.
SELECT id
FROM bots
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(bot_id)
FOR UPDATE;

-- name: ClaimLockedSessionRun :one
-- Ownership change: the only write that touches owner_id, fencing_token,
-- owner_since and live_generation. live_generation is stamped once here and
-- never updated, which keeps idx_session_runs_recovery a stable keyset cursor.
-- Fencing tokens come from a monotonic sequence, so a newer claim always
-- carries a strictly larger token than the one it replaces.
WITH target_session AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  JOIN bots bot ON bot.team_id = session.team_id AND bot.id = session.bot_id
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = sqlc.arg(session_id)
    AND session.bot_id = sqlc.arg(bot_id)
    AND session.deleted_at IS NULL
    AND bot.status <> 'deleting'
    AND (bot.runtime_reset_expires_at IS NULL OR bot.runtime_reset_expires_at <= clock_timestamp())
    AND (session.runtime_reset_expires_at IS NULL OR session.runtime_reset_expires_at <= clock_timestamp())
  FOR UPDATE OF session
)
UPDATE session_runs run
SET owner_id = sqlc.arg(owner_id),
    owner_since = now(),
    fencing_token = sqlc.arg(fencing_token),
    live_generation = sqlc.arg(live_generation),
    state = 'running',
    updated_at = now()
FROM target_session target
WHERE run.team_id = public.memoh_current_team_id()
  AND run.run_id = sqlc.arg(run_id)
  AND run.bot_id = sqlc.arg(bot_id)
  AND run.session_id = target.id
  AND run.state = 'accepted'
  AND run.fencing_token < sqlc.arg(fencing_token)
RETURNING run.*;

-- name: SetSessionRunWaitingDecision :one
UPDATE session_runs
SET state = 'waiting_decision',
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND run_id = sqlc.arg(run_id)
  AND fencing_token = sqlc.arg(fencing_token)
  AND state IN ('running', 'waiting_decision')
RETURNING *;

-- name: ReclaimWaitingDecisionSessionRun :one
-- Recovery advances both durable ownership and the backend incarnation while
-- preserving waiting_decision. The previous token is the CAS guard; the new
-- token must be strictly newer so a stale reaper can never move ownership
-- backwards.
UPDATE session_runs
SET owner_id = sqlc.arg(owner_id),
    owner_since = now(),
    fencing_token = sqlc.arg(new_fencing_token),
    live_generation = sqlc.arg(live_generation),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND run_id = sqlc.arg(run_id)
  AND state = 'waiting_decision'
  AND fencing_token = sqlc.arg(previous_fencing_token)
  AND fencing_token < sqlc.arg(new_fencing_token)
RETURNING *;

-- name: ResumeSessionRun :one
UPDATE session_runs
SET state = 'running',
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND run_id = sqlc.arg(run_id)
  AND fencing_token = sqlc.arg(fencing_token)
  AND state IN ('running', 'waiting_decision')
RETURNING *;

-- name: FinalizeSessionRun :one
-- Fenced idempotent terminal write, shared by the owner (completed / aborted /
-- failed) and the reaper (lost). Zero rows affected means the transition was
-- already applied or a newer owner superseded this token; neither is an error.
UPDATE session_runs
SET state = CASE
        WHEN sqlc.arg(state)::text = 'lost' AND abort_requested_at IS NOT NULL THEN 'aborted'
        ELSE sqlc.arg(state)::text
    END,
    error_code = CASE
        WHEN sqlc.arg(state)::text = 'lost' AND abort_requested_at IS NOT NULL THEN NULL
        ELSE sqlc.narg(error_code)::text
    END,
    error_message = CASE
        WHEN sqlc.arg(state)::text = 'lost' AND abort_requested_at IS NOT NULL THEN NULL
        ELSE sqlc.narg(error_message)::text
    END,
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND run_id = sqlc.arg(run_id)
  AND fencing_token = sqlc.arg(fencing_token)
  AND state IN ('accepted', 'running', 'waiting_decision')
RETURNING *;

-- name: LockActiveSessionRunForHistoryReset :one
-- The reset finalizer already holds the bot parent lock. Locking the target
-- run in a fresh statement makes the old fencing token/state an atomic
-- predicate for advancing the session persistence fence and terminalizing it.
SELECT *
FROM session_runs
WHERE team_id = public.memoh_current_team_id()
  AND run_id = sqlc.arg(run_id)
  AND bot_id = sqlc.arg(bot_id)
  AND session_id = sqlc.arg(session_id)
  AND fencing_token = sqlc.arg(fencing_token)
  AND state IN ('accepted', 'running', 'waiting_decision')
FOR UPDATE;

-- name: RequestSessionRunAbort :one
-- Not fenced: abort is a user intent that may arrive at any instance, and the
-- owner applies it. Repeating it keeps the first request time.
UPDATE session_runs
SET abort_requested_at = COALESCE(abort_requested_at, now()),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND run_id = sqlc.arg(run_id)
  AND state IN ('accepted', 'running', 'waiting_decision')
RETURNING *;

-- name: LockSessionRunForAgentStepCommit :one
-- Complete steps must precede abort intent; interrupted checkpoints only need
-- the same active owner because some cancellation paths do not record intent.
SELECT run_id
FROM session_runs
WHERE team_id = public.memoh_current_team_id()
  AND run_id = sqlc.arg(run_id)
  AND bot_id = sqlc.arg(bot_id)
  AND session_id = sqlc.arg(session_id)
  AND fencing_token = sqlc.arg(fencing_token)
  AND state IN ('running', 'waiting_decision')
  AND (sqlc.arg(interrupted)::boolean OR abort_requested_at IS NULL)
FOR UPDATE;

-- name: ListStaleGenerationSessionRuns :many
-- Fail-closed recovery sweep. Matching on "not the current generation" rather
-- than one specific old value means a backend lost twice in quick succession
-- cannot strand rows from the older incarnation. Orphans are excluded by
-- live_generation IS NOT NULL: they were never claimed.
--
-- A NULL cursor means "start from the beginning" and must be spelled out: the
-- keyset comparison below is NULL for every row when the cursor is NULL, so
-- without this branch the first batch of every sweep matches nothing, and a
-- sweep that never returns a full batch never advances past it either.
SELECT *
FROM session_runs
WHERE team_id = public.memoh_current_team_id()
  AND state IN ('accepted', 'running', 'waiting_decision')
  AND live_generation IS NOT NULL
  AND live_generation <> sqlc.arg(current_generation)
  AND (
    sqlc.narg(cursor_generation)::text IS NULL
    OR live_generation > sqlc.narg(cursor_generation)
    OR (live_generation = sqlc.narg(cursor_generation) AND run_id > sqlc.narg(cursor_run_id))
  )
ORDER BY live_generation, run_id
LIMIT sqlc.arg(batch_size);

-- name: ListOrphanedSessionRuns :many
-- Admission committed but the process died before reserving the run in the live
-- backend. No lease exists, so these never appear as expiry candidates and no
-- generation change implicates them. A single leader reaper reads this, so no
-- row locking is needed.
SELECT *
FROM session_runs
WHERE team_id = public.memoh_current_team_id()
  AND state = 'accepted'
  AND owner_id IS NULL
  AND created_at < now() - make_interval(secs => sqlc.arg(grace_seconds)::double precision)
ORDER BY created_at, run_id
LIMIT sqlc.arg(batch_size);

-- name: NextSessionRunFencingToken :one
SELECT nextval('session_runtime_fencing_token_seq')::bigint AS token;
