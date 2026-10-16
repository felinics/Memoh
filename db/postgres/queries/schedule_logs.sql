-- name: CreateScheduleLog :one
INSERT INTO schedule_logs (schedule_id, bot_id, session_id, started_at)
VALUES ($1, $2, sqlc.narg(session_id)::uuid, now())
RETURNING id, schedule_id, bot_id, session_id, status, result_text, error_message, error_code, usage, started_at, completed_at;

-- name: CompleteScheduleLog :one
UPDATE schedule_logs
SET status = $2,
    result_text = $3,
    error_message = '',
    error_code = $4,
    usage = $5,
    model_id = $6,
    completed_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = $1
RETURNING id, schedule_id, bot_id, session_id, status, result_text, error_message, error_code, usage, model_id, started_at, completed_at, team_id;

-- name: ListScheduleLogsByBot :many
SELECT id, schedule_id, bot_id, session_id, status, result_text, error_message, error_code, usage, started_at, completed_at
FROM schedule_logs
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1
ORDER BY started_at DESC
LIMIT $2 OFFSET $3;

-- name: CountScheduleLogsByBot :one
SELECT count(*) FROM schedule_logs WHERE team_id = public.memoh_current_team_id() AND bot_id = $1;

-- name: ListScheduleLogsBySchedule :many
SELECT id, schedule_id, bot_id, session_id, status, result_text, error_message, error_code, usage, started_at, completed_at
FROM schedule_logs
WHERE team_id = public.memoh_current_team_id() AND schedule_id = $1
ORDER BY started_at DESC
LIMIT $2 OFFSET $3;

-- name: CountScheduleLogsBySchedule :one
SELECT count(*) FROM schedule_logs WHERE team_id = public.memoh_current_team_id() AND schedule_id = $1;

-- name: DeleteScheduleLogsByBot :exec
DELETE FROM schedule_logs WHERE team_id = public.memoh_current_team_id() AND bot_id = $1;

-- name: DeleteScheduleLogsBySchedule :exec
DELETE FROM schedule_logs WHERE team_id = public.memoh_current_team_id() AND schedule_id = $1;
