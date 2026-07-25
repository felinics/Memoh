-- name: CreateSearchProvider :one
INSERT INTO model.search_providers (name, provider, config, enable)
VALUES (
  sqlc.arg(name),
  sqlc.arg(provider),
  sqlc.arg(config),
  sqlc.arg(enable)
)
RETURNING *;

-- name: GetSearchProviderByID :one
SELECT * FROM model.search_providers WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: GetSearchProviderByName :one
SELECT * FROM model.search_providers WHERE team_id = iam.memoh_current_team_id() AND name = sqlc.arg(name);

-- name: ListSearchProviders :many
SELECT * FROM model.search_providers
WHERE team_id = iam.memoh_current_team_id()
ORDER BY created_at DESC;

-- name: ListSearchProvidersByProvider :many
SELECT * FROM model.search_providers
WHERE team_id = iam.memoh_current_team_id() AND provider = sqlc.arg(provider)
ORDER BY created_at DESC;

-- name: UpdateSearchProvider :one
UPDATE model.search_providers
SET
  name = sqlc.arg(name),
  provider = sqlc.arg(provider),
  config = sqlc.arg(config),
  enable = sqlc.arg(enable),
  updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id)
RETURNING *;

-- name: DeleteSearchProvider :exec
DELETE FROM model.search_providers WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);
