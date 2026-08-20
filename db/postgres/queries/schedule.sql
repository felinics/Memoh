-- name: CreateSchedule :one
INSERT INTO schedule (
  name, description, pattern, max_calls, enabled, command, bot_id,
  run_target, target_session_id, runtime_type, bot_agent_id, acp_agent_id, model_id, acp_model_id, reasoning_effort, workdir_id
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
  sqlc.narg(workdir_id)::uuid
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
