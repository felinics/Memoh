-- name: CreateUserRuntime :one
INSERT INTO runtime.user_runtimes (user_id, name, api_token)
VALUES (sqlc.arg(user_id), sqlc.arg(name), sqlc.arg(api_token))
RETURNING *;

-- name: GetUserRuntimeByAPIToken :one
SELECT * FROM runtime.user_runtimes
WHERE team_id = iam.memoh_current_team_id()
  AND api_token = sqlc.arg(api_token)
  AND revoked_at IS NULL;

-- name: ListUserRuntimes :many
SELECT * FROM runtime.user_runtimes
WHERE team_id = iam.memoh_current_team_id()
  AND user_id = sqlc.arg(user_id) AND revoked_at IS NULL
ORDER BY created_at ASC, id ASC;

-- name: RevokeUserRuntime :one
UPDATE runtime.user_runtimes
SET revoked_at = now(), updated_at = now()
WHERE team_id = iam.memoh_current_team_id()
  AND id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND revoked_at IS NULL
RETURNING *;
