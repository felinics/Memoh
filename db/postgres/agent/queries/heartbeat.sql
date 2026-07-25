-- name: CreateHeartbeatLog :one
INSERT INTO agent.bot_heartbeat_logs (bot_id, session_id, started_at)
VALUES ($1, sqlc.narg(session_id)::uuid, now())
RETURNING id, bot_id, session_id, status, result_text, error_message, usage, started_at, completed_at;

-- name: CompleteHeartbeatLog :one
UPDATE agent.bot_heartbeat_logs
SET status = $2,
    result_text = $3,
    error_message = $4,
    usage = $5,
    model_id = $6,
    completed_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = $1
RETURNING id, bot_id, session_id, status, result_text, error_message, usage, model_id, started_at, completed_at, team_id;

-- name: ListHeartbeatLogsByBot :many
SELECT id, bot_id, session_id, status, result_text, error_message, usage, started_at, completed_at
FROM agent.bot_heartbeat_logs
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1
ORDER BY started_at DESC
LIMIT $2 OFFSET $3;

-- name: CountHeartbeatLogsByBot :one
SELECT count(*) FROM agent.bot_heartbeat_logs WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1;

-- name: DeleteHeartbeatLogsByBot :exec
DELETE FROM agent.bot_heartbeat_logs WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1;
