-- name: ListBotChannelAdmins :many
SELECT
  a.id,
  a.bot_id,
  a.channel_identity_id,
  a.granted,
  a.created_by_user_id,
  a.created_at,
  a.updated_at
FROM api.bot_channel_admins a
WHERE a.team_id = iam.memoh_current_team_id() AND a.bot_id = $1
ORDER BY a.created_at DESC;

-- name: UpsertBotChannelAdmin :one
INSERT INTO api.bot_channel_admins (
  bot_id,
  channel_identity_id,
  granted,
  created_by_user_id
)
VALUES (
  $1,
  $2,
  $3,
  sqlc.narg(created_by_user_id)::uuid
)
ON CONFLICT (team_id, bot_id, channel_identity_id) DO UPDATE
  SET granted = EXCLUDED.granted,
      updated_at = now()
RETURNING *;

-- name: DeleteBotChannelAdmin :exec
DELETE FROM api.bot_channel_admins
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND channel_identity_id = $2;

-- name: GetBotChannelAdmin :one
SELECT id, bot_id, channel_identity_id, granted, created_by_user_id, created_at, updated_at, team_id
FROM api.bot_channel_admins
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND channel_identity_id = $2;
