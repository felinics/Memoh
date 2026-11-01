-- name: CreateStorageProvider :one
INSERT INTO storage_providers (name, provider, config)
VALUES (sqlc.arg(name), sqlc.arg(provider), sqlc.arg(config))
RETURNING *;

-- name: GetStorageProviderByID :one
SELECT * FROM storage_providers WHERE team_id = public.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: GetStorageProviderByName :one
SELECT * FROM storage_providers WHERE team_id = public.memoh_current_team_id() AND name = sqlc.arg(name);

-- name: ListStorageProviders :many
SELECT * FROM storage_providers WHERE team_id = public.memoh_current_team_id() ORDER BY created_at DESC;

-- name: UpsertBotStorageBinding :one
INSERT INTO bot_storage_bindings (bot_id, storage_provider_id, base_path)
VALUES (sqlc.arg(bot_id), sqlc.arg(storage_provider_id), sqlc.arg(base_path))
ON CONFLICT (team_id, bot_id) DO UPDATE SET
  storage_provider_id = EXCLUDED.storage_provider_id,
  base_path = EXCLUDED.base_path,
  updated_at = now()
RETURNING *;

-- name: GetBotStorageBinding :one
SELECT * FROM bot_storage_bindings WHERE team_id = public.memoh_current_team_id() AND bot_id = sqlc.arg(bot_id);

-- name: CreateMessageAsset :one
INSERT INTO bot_history_message_assets (
  message_id, role, ordinal, content_hash, mime, size_bytes, storage_key, name, metadata
)
VALUES (
  sqlc.arg(message_id),
  sqlc.arg(role),
  sqlc.arg(ordinal),
  sqlc.arg(content_hash),
  COALESCE(sqlc.arg(mime)::text, ''),
  sqlc.arg(size_bytes),
  COALESCE(sqlc.arg(storage_key)::text, ''),
  sqlc.arg(name),
  sqlc.arg(metadata)
)
ON CONFLICT (team_id, message_id, content_hash) DO UPDATE SET
  role = EXCLUDED.role,
  ordinal = EXCLUDED.ordinal,
  mime = CASE WHEN EXCLUDED.mime <> '' THEN EXCLUDED.mime ELSE bot_history_message_assets.mime END,
  size_bytes = CASE WHEN EXCLUDED.size_bytes > 0 THEN EXCLUDED.size_bytes ELSE bot_history_message_assets.size_bytes END,
  storage_key = CASE WHEN EXCLUDED.storage_key <> '' THEN EXCLUDED.storage_key ELSE bot_history_message_assets.storage_key END,
  name = EXCLUDED.name,
  metadata = EXCLUDED.metadata
RETURNING id, message_id, role, ordinal, content_hash, mime, size_bytes, storage_key, name, metadata, created_at;

-- name: ListMessageAssets :many
SELECT id AS rel_id, message_id, role, ordinal, content_hash, mime, size_bytes, storage_key, name, metadata
FROM bot_history_message_assets
WHERE team_id = public.memoh_current_team_id() AND message_id = sqlc.arg(message_id)
ORDER BY ordinal ASC;

-- name: ListMessageAssetsBatch :many
SELECT id AS rel_id, message_id, role, ordinal, content_hash, mime, size_bytes, storage_key, name, metadata
FROM bot_history_message_assets
WHERE team_id = public.memoh_current_team_id() AND message_id = ANY(sqlc.arg(message_ids)::uuid[])
ORDER BY message_id, ordinal ASC;

-- name: CountMessageAssetsByBot :one
SELECT COUNT(*)
FROM bot_history_message_assets a
JOIN bot_history_messages m ON m.id = a.message_id
WHERE a.team_id = public.memoh_current_team_id() AND m.team_id = public.memoh_current_team_id() AND m.bot_id = sqlc.arg(bot_id);

-- name: DeleteMessageAssets :exec
DELETE FROM bot_history_message_assets WHERE team_id = public.memoh_current_team_id() AND message_id = sqlc.arg(message_id);
