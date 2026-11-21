-- name: CreateSchedule :one
INSERT INTO schedule (
  name, description, pattern, max_calls, enabled, command, bot_id,
  run_target, target_session_id, runtime_type, bot_agent_id, acp_agent_id, model_id, acp_model_id, reasoning_effort, workdir_id, max_run_seconds,
  notify_policy, contact_via_channels
)
VALUES (
  $1, $2, $3, $4, $5, $6, $7,
  $8,
  sqlc.narg(target_session_id)::uuid,
  sqlc.narg(runtime_type)::text,
  sqlc.narg(bot_agent_id)::uuid,
  sqlc.narg(acp_agent_id)::text,
  sqlc.narg(model_id)::uuid,
  sqlc.narg(acp_model_id)::text,
  sqlc.narg(reasoning_effort)::text,
  sqlc.narg(workdir_id)::uuid,
 COALESCE(NULLIF(sqlc.arg(max_run_seconds)::integer, 0), 3600),
  sqlc.arg(notify_policy)::text,
  sqlc.arg(contact_via_channels)::bool
)
RETURNING *;

-- name: GetScheduleByID :one
SELECT *
FROM schedule
WHERE team_id = public.memoh_current_team_id() AND id = $1;

-- name: ListSchedulesByBot :many
SELECT *
FROM schedule
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1
ORDER BY created_at DESC;

-- name: ListEnabledSchedules :many
SELECT *
FROM schedule
WHERE team_id = public.memoh_current_team_id() AND enabled = true
ORDER BY created_at DESC;

-- name: UpdateSchedule :one
UPDATE schedule
SET name = $2,
    description = $3,
    pattern = $4,
    max_calls = $5,
    enabled = $6,
    command = $7,
    run_target = $8,
    target_session_id = sqlc.narg(target_session_id)::uuid,
    runtime_type = sqlc.narg(runtime_type)::text,
    bot_agent_id = sqlc.narg(bot_agent_id)::uuid,
    acp_agent_id = sqlc.narg(acp_agent_id)::text,
    model_id = sqlc.narg(model_id)::uuid,
    acp_model_id = sqlc.narg(acp_model_id)::text,
    reasoning_effort = sqlc.narg(reasoning_effort)::text,
    workdir_id = sqlc.narg(workdir_id)::uuid,
    max_run_seconds = COALESCE(NULLIF(sqlc.arg(max_run_seconds)::integer, 0), 3600),
    notify_policy = sqlc.arg(notify_policy)::text,
    contact_via_channels = sqlc.arg(contact_via_channels)::bool,
    updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = $1
RETURNING *;

-- name: DeleteSchedule :exec
DELETE FROM schedule
WHERE team_id = public.memoh_current_team_id() AND id = $1;

-- name: DisableSchedule :one
UPDATE schedule
SET enabled = false,
    updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = $1
RETURNING *;

-- name: IncrementScheduleCalls :one
UPDATE schedule
SET current_calls = current_calls + 1,
    enabled = CASE
      WHEN max_calls IS NOT NULL AND current_calls + 1 >= max_calls THEN false
      ELSE enabled
    END,
    updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = $1
RETURNING *;

-- name: GetActiveScheduleFireForSession :one
-- The fire of a schedule that is running in a session right now, with the
-- schedule's notification settings and the session's latest attention: the
-- notify_user tool registers only for a fire whose schedule asks for it, and
-- raises attention once per fire.
SELECT l.id AS log_id,
       l.started_at,
       l.schedule_id,
       sc.notify_policy,
       sc.contact_via_channels,
       s.attention_kind,
       s.attention_at
FROM schedule_logs l
JOIN schedule sc ON sc.team_id = l.team_id AND sc.id = l.schedule_id
JOIN bot_sessions s ON s.team_id = l.team_id AND s.id = l.session_id
WHERE l.team_id = public.memoh_current_team_id()
  AND l.session_id = sqlc.arg(session_id)
  AND l.completed_at IS NULL
ORDER BY l.started_at DESC
LIMIT 1;

-- name: GetScheduleNotifyPolicyForRun :one
-- Whether a run was fired by a schedule, and that schedule's notify_policy. A
-- run's input is its submission, which a schedule trigger writes as
-- {"kind": "schedule", "schedule_id": ...}; any other run reads as not
-- scheduled. A schedule deleted since reads as the default policy.
SELECT (COALESCE(r.input_json->>'kind', '') = 'schedule')::bool AS scheduled,
       COALESCE(sc.notify_policy, 'important')::text AS notify_policy
FROM session_runs r
LEFT JOIN schedule sc
  ON sc.team_id = r.team_id AND sc.id::text = r.input_json->>'schedule_id'
WHERE r.team_id = public.memoh_current_team_id()
  AND r.run_id = sqlc.arg(run_id);
