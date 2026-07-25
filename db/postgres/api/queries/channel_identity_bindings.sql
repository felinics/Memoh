-- name: CreateChannelLinkCode :one
INSERT INTO api.channel_link_codes (token, user_id, channel_type, expires_at)
VALUES ($1, $2, sqlc.narg(channel_type)::text, $3)
RETURNING token, user_id, channel_type, expires_at, consumed_at, consumed_channel_identity_id, created_at, team_id;

-- name: GetChannelLinkCodeByToken :one
SELECT token, user_id, channel_type, expires_at, consumed_at, consumed_channel_identity_id, created_at, team_id
FROM api.channel_link_codes
WHERE team_id = iam.memoh_current_team_id() AND token = $1;

-- name: RedeemChannelLinkCode :one
WITH claimed AS (
  UPDATE api.channel_link_codes
  SET consumed_at = now(),
      consumed_channel_identity_id = $2
  WHERE team_id = iam.memoh_current_team_id()
    AND token = $1
    AND consumed_at IS NULL
    AND expires_at > now()
  RETURNING user_id
)
INSERT INTO api.user_channel_identity_bindings (user_id, channel_identity_id)
SELECT user_id, $2
FROM claimed
ON CONFLICT (team_id, user_id, channel_identity_id) DO UPDATE
  SET updated_at = now()
RETURNING id, user_id, channel_identity_id, created_at, updated_at, team_id;

-- name: MarkChannelLinkCodeConsumed :one
UPDATE api.channel_link_codes
SET consumed_at = now(),
    consumed_channel_identity_id = $2
WHERE team_id = iam.memoh_current_team_id() AND token = $1 AND consumed_at IS NULL AND expires_at > now()
RETURNING token, user_id, channel_type, expires_at, consumed_at, consumed_channel_identity_id, created_at, team_id;

-- name: UpsertUserChannelIdentityBinding :one
INSERT INTO api.user_channel_identity_bindings (user_id, channel_identity_id)
VALUES ($1, $2)
ON CONFLICT (team_id, user_id, channel_identity_id) DO UPDATE
  SET updated_at = now()
RETURNING id, user_id, channel_identity_id, created_at, updated_at, team_id;

-- name: DeleteUserChannelIdentityBinding :exec
DELETE FROM api.user_channel_identity_bindings
WHERE team_id = iam.memoh_current_team_id() AND user_id = $1 AND channel_identity_id = $2;

-- name: ListUserIDsByChannelIdentity :many
SELECT user_id
FROM api.user_channel_identity_bindings
WHERE team_id = iam.memoh_current_team_id() AND channel_identity_id = $1;

-- name: ListChannelIdentityBindingsForUser :many
SELECT id, user_id, channel_identity_id, created_at, updated_at, team_id
FROM api.user_channel_identity_bindings
WHERE team_id = iam.memoh_current_team_id() AND user_id = $1
ORDER BY created_at DESC;

-- name: ListChannelIdentityBindingsForBot :many
SELECT DISTINCT
  b.id,
  b.user_id,
  b.channel_identity_id,
  b.created_at,
  b.updated_at,
  b.team_id
FROM api.user_channel_identity_bindings b
INNER JOIN api.bot_user_grants g
  ON g.user_id = b.user_id
 AND g.bot_id = $1
 AND g.subject_type = 'user'
 AND g.team_id = iam.memoh_current_team_id()
WHERE b.team_id = iam.memoh_current_team_id()
ORDER BY b.created_at DESC;
