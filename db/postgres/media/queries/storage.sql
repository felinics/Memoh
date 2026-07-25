-- name: CreateStorageProvider :one
INSERT INTO media.storage_providers (name, provider, config)
VALUES (sqlc.arg(name), sqlc.arg(provider), sqlc.arg(config))
RETURNING *;

-- name: GetStorageProviderByID :one
SELECT * FROM media.storage_providers WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: GetStorageProviderByName :one
SELECT * FROM media.storage_providers WHERE team_id = iam.memoh_current_team_id() AND name = sqlc.arg(name);

-- name: ListStorageProviders :many
SELECT * FROM media.storage_providers WHERE team_id = iam.memoh_current_team_id() ORDER BY created_at DESC;

-- name: UpsertBotStorageBinding :one
INSERT INTO media.bot_storage_bindings (bot_id, storage_provider_id, base_path)
VALUES (sqlc.arg(bot_id), sqlc.arg(storage_provider_id), sqlc.arg(base_path))
ON CONFLICT (team_id, bot_id) DO UPDATE SET
  storage_provider_id = EXCLUDED.storage_provider_id,
  base_path = EXCLUDED.base_path,
  updated_at = now()
RETURNING *;

-- name: GetBotStorageBinding :one
SELECT * FROM media.bot_storage_bindings WHERE team_id = iam.memoh_current_team_id() AND bot_id = sqlc.arg(bot_id);
