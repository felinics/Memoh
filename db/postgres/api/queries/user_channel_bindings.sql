-- name: GetUserChannelBinding :one
SELECT id, user_id, channel_type, config, created_at, updated_at, team_id
FROM api.user_channel_bindings
WHERE team_id = iam.memoh_current_team_id() AND user_id = $1 AND channel_type = $2
LIMIT 1;

-- name: UpsertUserChannelBinding :one
INSERT INTO api.user_channel_bindings (user_id, channel_type, config)
VALUES ($1, $2, $3)
ON CONFLICT (team_id, user_id, channel_type)
DO UPDATE SET
  config = EXCLUDED.config,
  updated_at = now()
RETURNING id, user_id, channel_type, config, created_at, updated_at, team_id;

-- name: ListUserChannelBindingsByPlatform :many
SELECT id, user_id, channel_type, config, created_at, updated_at, team_id
FROM api.user_channel_bindings
WHERE team_id = iam.memoh_current_team_id() AND channel_type = $1
ORDER BY created_at DESC;
