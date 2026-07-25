-- name: DeleteBotChannelConfig :exec
DELETE FROM channel.bot_channel_configs
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND channel_type = $2;

-- name: GetBotChannelConfig :one
SELECT id, bot_id, channel_type, credentials, external_identity, self_identity, routing, capabilities, disabled, verified_at, created_at, updated_at, team_id
FROM channel.bot_channel_configs
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND channel_type = $2
LIMIT 1;

-- name: GetBotChannelConfigByExternalIdentity :one
SELECT id, bot_id, channel_type, credentials, external_identity, self_identity, routing, capabilities, disabled, verified_at, created_at, updated_at, team_id
FROM channel.bot_channel_configs
WHERE team_id = iam.memoh_current_team_id() AND channel_type = $1 AND external_identity = $2
LIMIT 1;

-- name: UpsertBotChannelConfig :one
INSERT INTO channel.bot_channel_configs (
  bot_id, channel_type, credentials, external_identity, self_identity, routing, capabilities, disabled, verified_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (team_id, bot_id, channel_type)
DO UPDATE SET
  credentials = EXCLUDED.credentials,
  external_identity = EXCLUDED.external_identity,
  self_identity = EXCLUDED.self_identity,
  routing = EXCLUDED.routing,
  capabilities = EXCLUDED.capabilities,
  disabled = EXCLUDED.disabled,
  verified_at = EXCLUDED.verified_at,
  updated_at = now()
RETURNING id, bot_id, channel_type, credentials, external_identity, self_identity, routing, capabilities, disabled, verified_at, created_at, updated_at, team_id;

-- name: UpdateBotChannelConfigDisabled :one
UPDATE channel.bot_channel_configs
SET
  disabled = $3,
  updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND channel_type = $2
RETURNING id, bot_id, channel_type, credentials, external_identity, self_identity, routing, capabilities, disabled, verified_at, created_at, updated_at, team_id;

-- name: SaveMatrixSyncSinceToken :execrows
UPDATE channel.bot_channel_configs
SET routing = COALESCE(routing, '{}'::jsonb) || jsonb_build_object(
  '_matrix',
  COALESCE(routing->'_matrix', '{}'::jsonb) || jsonb_build_object('since_token', sqlc.arg(since_token)::text)
)
WHERE team_id = iam.memoh_current_team_id() AND id = $1;

-- name: ListBotChannelConfigsByType :many
SELECT id, bot_id, channel_type, credentials, external_identity, self_identity, routing, capabilities, disabled, verified_at, created_at, updated_at, team_id
FROM channel.bot_channel_configs
WHERE team_id = iam.memoh_current_team_id() AND channel_type = $1
ORDER BY created_at DESC;
