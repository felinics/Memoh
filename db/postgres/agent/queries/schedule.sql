-- name: CreateSchedule :one
INSERT INTO agent.schedule (name, description, pattern, max_calls, enabled, command, bot_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, name, description, pattern, max_calls, current_calls, created_at, updated_at, enabled, command, bot_id, team_id;

-- name: GetScheduleByID :one
SELECT id, name, description, pattern, max_calls, current_calls, created_at, updated_at, enabled, command, bot_id, team_id
FROM agent.schedule
WHERE team_id = iam.memoh_current_team_id() AND id = $1;

-- name: ListSchedulesByBot :many
SELECT id, name, description, pattern, max_calls, current_calls, created_at, updated_at, enabled, command, bot_id, team_id
FROM agent.schedule
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1
ORDER BY created_at DESC;

-- name: ListEnabledSchedules :many
SELECT id, name, description, pattern, max_calls, current_calls, created_at, updated_at, enabled, command, bot_id, team_id
FROM agent.schedule
WHERE team_id = iam.memoh_current_team_id() AND enabled = true
ORDER BY created_at DESC;

-- name: UpdateSchedule :one
UPDATE agent.schedule
SET name = $2,
    description = $3,
    pattern = $4,
    max_calls = $5,
    enabled = $6,
    command = $7,
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = $1
RETURNING id, name, description, pattern, max_calls, current_calls, created_at, updated_at, enabled, command, bot_id, team_id;

-- name: DeleteSchedule :exec
DELETE FROM agent.schedule
WHERE team_id = iam.memoh_current_team_id() AND id = $1;

-- name: IncrementScheduleCalls :one
UPDATE agent.schedule
SET current_calls = current_calls + 1,
    enabled = CASE
      WHEN max_calls IS NOT NULL AND current_calls + 1 >= max_calls THEN false
      ELSE enabled
    END,
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = $1
RETURNING id, name, description, pattern, max_calls, current_calls, created_at, updated_at, enabled, command, bot_id, team_id;
