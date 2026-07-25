-- name: CreateProvider :one
INSERT INTO model.providers (name, client_type, icon, enable, config, metadata)
VALUES (
  sqlc.arg(name),
  sqlc.arg(client_type),
  sqlc.arg(icon),
  sqlc.arg(enable),
  sqlc.arg(config),
  sqlc.arg(metadata)
)
RETURNING *;

-- name: CreateProviderFromTemplate :one
INSERT INTO model.providers (provider_template_id, name, client_type, icon, enable, config, metadata)
VALUES (
  sqlc.arg(provider_template_id),
  sqlc.arg(name),
  sqlc.arg(client_type),
  sqlc.arg(icon),
  sqlc.arg(enable),
  sqlc.arg(config),
  sqlc.arg(metadata)
)
RETURNING *;

-- name: GetProviderByID :one
SELECT * FROM model.providers WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: GetProviderByName :one
SELECT * FROM model.providers WHERE team_id = iam.memoh_current_team_id() AND name = sqlc.arg(name);

-- name: GetProviderByClientType :one
SELECT * FROM model.providers WHERE team_id = iam.memoh_current_team_id() AND client_type = sqlc.arg(client_type);

-- name: ListProviders :many
SELECT * FROM model.providers
WHERE team_id = iam.memoh_current_team_id() AND client_type NOT IN (
  'edge-speech',
  'openai-speech',
  'openai-transcription',
  'openrouter-speech',
  'openrouter-transcription',
  'elevenlabs-speech',
  'elevenlabs-transcription',
  'deepgram-speech',
  'deepgram-transcription',
  'minimax-speech',
  'volcengine-speech',
  'alibabacloud-speech',
  'microsoft-speech',
  'google-speech',
  'google-transcription',
  'openrouter-video',
  'modelark-video',
  'volcengine-video'
)
ORDER BY created_at DESC;

-- name: UpdateProvider :one
UPDATE model.providers
SET
  name = sqlc.arg(name),
  client_type = sqlc.arg(client_type),
  icon = sqlc.arg(icon),
  enable = sqlc.arg(enable),
  config = sqlc.arg(config),
  metadata = sqlc.arg(metadata),
  updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id)
RETURNING *;

-- name: DeleteProvider :exec
DELETE FROM model.providers WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: CountProviders :one
SELECT COUNT(*)
FROM model.providers
WHERE team_id = iam.memoh_current_team_id() AND client_type NOT IN (
  'edge-speech',
  'openai-speech',
  'openai-transcription',
  'openrouter-speech',
  'openrouter-transcription',
  'elevenlabs-speech',
  'elevenlabs-transcription',
  'deepgram-speech',
  'deepgram-transcription',
  'minimax-speech',
  'volcengine-speech',
  'alibabacloud-speech',
  'microsoft-speech',
  'google-speech',
  'google-transcription',
  'openrouter-video',
  'modelark-video',
  'volcengine-video'
);

-- name: CreateModel :one
INSERT INTO model.models (model_id, name, provider_id, type, enable, config)
VALUES (
  sqlc.arg(model_id),
  sqlc.arg(name),
  sqlc.arg(provider_id),
  sqlc.arg(type),
  sqlc.arg(enable),
  sqlc.arg(config)
)
RETURNING *;

-- name: GetModelByID :one
SELECT * FROM model.models WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: GetModelByModelID :one
SELECT * FROM model.models WHERE team_id = iam.memoh_current_team_id() AND model_id = sqlc.arg(model_id);

-- name: ListModelsByModelID :many
SELECT * FROM model.models
WHERE team_id = iam.memoh_current_team_id() AND model_id = sqlc.arg(model_id)
ORDER BY created_at DESC;

-- name: ListModels :many
SELECT * FROM model.models
WHERE team_id = iam.memoh_current_team_id() AND type NOT IN ('speech', 'transcription', 'video')
ORDER BY created_at DESC;

-- name: ListModelsByType :many
SELECT * FROM model.models
WHERE team_id = iam.memoh_current_team_id() AND type = sqlc.arg(type)
ORDER BY created_at DESC;

-- name: ListModelsByProviderID :many
SELECT * FROM model.models
WHERE team_id = iam.memoh_current_team_id() AND provider_id = sqlc.arg(provider_id)
  AND type NOT IN ('speech', 'transcription', 'video')
ORDER BY created_at DESC;

-- name: ListModelsByProviderIDAndType :many
SELECT * FROM model.models
WHERE team_id = iam.memoh_current_team_id() AND provider_id = sqlc.arg(provider_id)
  AND type = sqlc.arg(type)
ORDER BY created_at DESC;

-- name: ListModelsByProviderClientType :many
SELECT m.*
FROM model.models m
JOIN model.providers p ON m.provider_id = p.id
WHERE m.team_id = iam.memoh_current_team_id() AND p.team_id = iam.memoh_current_team_id() AND p.client_type = sqlc.arg(client_type)
ORDER BY m.created_at DESC;

-- name: UpdateModel :one
UPDATE model.models
SET
  model_id = sqlc.arg(model_id),
  name = sqlc.arg(name),
  provider_id = sqlc.arg(provider_id),
  type = sqlc.arg(type),
  enable = sqlc.arg(enable),
  config = sqlc.arg(config),
  updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id)
RETURNING *;

-- name: DeleteModel :exec
DELETE FROM model.models WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: DeleteModelByModelID :exec
DELETE FROM model.models WHERE team_id = iam.memoh_current_team_id() AND model_id = sqlc.arg(model_id);

-- name: DeleteModelByProviderIDAndModelID :exec
DELETE FROM model.models
WHERE team_id = iam.memoh_current_team_id() AND provider_id = sqlc.arg(provider_id)
  AND model_id = sqlc.arg(model_id);

-- name: DeleteModelByProviderAndType :exec
DELETE FROM model.models
WHERE team_id = iam.memoh_current_team_id() AND provider_id = sqlc.arg(provider_id)
  AND model_id = sqlc.arg(model_id)
  AND type = sqlc.arg(type);

-- name: CountModels :one
SELECT COUNT(*) FROM model.models
WHERE team_id = iam.memoh_current_team_id() AND type NOT IN ('speech', 'transcription', 'video');

-- name: CountModelsByType :one
SELECT COUNT(*) FROM model.models WHERE team_id = iam.memoh_current_team_id() AND type = sqlc.arg(type);

-- name: UpsertRegistryProvider :one
INSERT INTO model.providers (name, client_type, icon, enable, config, metadata)
VALUES (sqlc.arg(name), sqlc.arg(client_type), sqlc.arg(icon), false, sqlc.arg(config), '{}')
ON CONFLICT (team_id, name) DO UPDATE SET
  icon = EXCLUDED.icon,
  client_type = EXCLUDED.client_type,
  updated_at = now()
RETURNING *;

-- name: UpsertRegistryModel :one
INSERT INTO model.models (model_id, name, provider_id, type, enable, config)
VALUES (sqlc.arg(model_id), sqlc.arg(name), sqlc.arg(provider_id), sqlc.arg(type), false, sqlc.arg(config))
ON CONFLICT (team_id, provider_id, model_id) DO UPDATE SET
  name = EXCLUDED.name,
  type = EXCLUDED.type,
  config = CASE
    WHEN model.models.config ? 'description'
      THEN EXCLUDED.config || jsonb_build_object('description', model.models.config -> 'description')
    ELSE EXCLUDED.config
  END,
  updated_at = now()
RETURNING *;

-- name: ListEnabledModels :many
SELECT m.*
FROM model.models m
JOIN model.providers p ON m.provider_id = p.id
WHERE m.team_id = iam.memoh_current_team_id() AND p.team_id = iam.memoh_current_team_id() AND p.enable = true
  AND m.enable = true
  AND m.type NOT IN ('speech', 'transcription', 'video')
ORDER BY m.created_at DESC;

-- name: ListEnabledModelsByType :many
SELECT m.*
FROM model.models m
JOIN model.providers p ON m.provider_id = p.id
WHERE m.team_id = iam.memoh_current_team_id() AND p.team_id = iam.memoh_current_team_id() AND p.enable = true
  AND m.enable = true
  AND m.type = sqlc.arg(type)
ORDER BY m.created_at DESC;

-- name: ListEnabledModelsByProviderClientType :many
SELECT m.*
FROM model.models m
JOIN model.providers p ON m.provider_id = p.id
WHERE m.team_id = iam.memoh_current_team_id() AND p.team_id = iam.memoh_current_team_id() AND p.enable = true
  AND m.enable = true
  AND p.client_type = sqlc.arg(client_type)
ORDER BY m.created_at DESC;

-- name: CreateModelVariant :one
INSERT INTO model.model_variants (model_uuid, variant_id, weight, metadata)
VALUES (
  sqlc.arg(model_uuid),
  sqlc.arg(variant_id),
  sqlc.arg(weight),
  sqlc.arg(metadata)
)
RETURNING *;

-- name: ListModelVariantsByModelUUID :many
SELECT * FROM model.model_variants
WHERE team_id = iam.memoh_current_team_id() AND model_uuid = sqlc.arg(model_uuid)
ORDER BY weight DESC, created_at DESC;

-- name: GetSpeechModelWithProvider :one
SELECT
  m.*,
  p.client_type AS provider_type
FROM model.models m
JOIN model.providers p ON p.id = m.provider_id
WHERE m.team_id = iam.memoh_current_team_id() AND p.team_id = iam.memoh_current_team_id() AND m.id = sqlc.arg(id)
  AND m.type = 'speech';

-- name: ListSpeechProviders :many
SELECT * FROM model.providers
WHERE team_id = iam.memoh_current_team_id() AND client_type IN (
  'edge-speech',
  'openai-speech',
  'openrouter-speech',
  'elevenlabs-speech',
  'deepgram-speech',
  'minimax-speech',
  'volcengine-speech',
  'alibabacloud-speech',
  'microsoft-speech'
)
ORDER BY created_at DESC;

-- name: ListTranscriptionProviders :many
SELECT * FROM model.providers
WHERE team_id = iam.memoh_current_team_id() AND client_type IN (
  'openai-transcription',
  'openrouter-transcription',
  'elevenlabs-transcription',
  'deepgram-transcription',
  'google-transcription'
)
ORDER BY created_at DESC;

-- name: ListSpeechModels :many
SELECT m.*,
  p.client_type AS provider_type
FROM model.models m
JOIN model.providers p ON p.id = m.provider_id
WHERE m.team_id = iam.memoh_current_team_id() AND p.team_id = iam.memoh_current_team_id() AND m.type = 'speech'
  AND m.enable = true
ORDER BY m.created_at DESC;

-- name: ListSpeechModelsByProviderID :many
SELECT * FROM model.models
WHERE team_id = iam.memoh_current_team_id() AND provider_id = sqlc.arg(provider_id)
  AND type = 'speech'
  AND enable = true
ORDER BY created_at DESC;

-- name: GetModelByProviderAndModelID :one
SELECT * FROM model.models
WHERE team_id = iam.memoh_current_team_id() AND provider_id = sqlc.arg(provider_id)
  AND model_id = sqlc.arg(model_id)
LIMIT 1;

-- name: GetTranscriptionModelWithProvider :one
SELECT
  m.*,
  p.client_type AS provider_type
FROM model.models m
JOIN model.providers p ON p.id = m.provider_id
WHERE m.team_id = iam.memoh_current_team_id() AND p.team_id = iam.memoh_current_team_id() AND m.id = sqlc.arg(id)
  AND m.type = 'transcription';

-- name: ListTranscriptionModels :many
SELECT m.*,
  p.client_type AS provider_type
FROM model.models m
JOIN model.providers p ON p.id = m.provider_id
WHERE m.team_id = iam.memoh_current_team_id() AND p.team_id = iam.memoh_current_team_id() AND m.type = 'transcription'
  AND m.enable = true
ORDER BY m.created_at DESC;

-- name: ListTranscriptionModelsByProviderID :many
SELECT * FROM model.models
WHERE team_id = iam.memoh_current_team_id() AND provider_id = sqlc.arg(provider_id)
  AND type = 'transcription'
  AND enable = true
ORDER BY created_at DESC;

-- name: GetVideoModelWithProvider :one
SELECT
  m.*,
  p.client_type AS provider_type
FROM model.models m
JOIN model.providers p ON p.id = m.provider_id
WHERE m.team_id = iam.memoh_current_team_id() AND p.team_id = iam.memoh_current_team_id() AND m.id = sqlc.arg(id)
  AND m.type = 'video';

-- name: ListVideoProviders :many
SELECT * FROM model.providers
WHERE team_id = iam.memoh_current_team_id() AND client_type IN (
  'openrouter-video',
  'modelark-video',
  'volcengine-video'
)
ORDER BY created_at DESC;

-- name: ListVideoModels :many
SELECT m.*,
  p.client_type AS provider_type
FROM model.models m
JOIN model.providers p ON p.id = m.provider_id
WHERE m.team_id = iam.memoh_current_team_id() AND p.team_id = iam.memoh_current_team_id() AND m.type = 'video'
  AND m.enable = true
ORDER BY m.created_at DESC;

-- name: ListVideoModelsByProviderID :many
SELECT * FROM model.models
WHERE team_id = iam.memoh_current_team_id() AND provider_id = sqlc.arg(provider_id)
  AND type = 'video'
  AND enable = true
ORDER BY created_at DESC;
