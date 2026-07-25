-- name: CreateFetchProvider :one
INSERT INTO model.fetch_providers (name, provider, config, enable)
VALUES (
  sqlc.arg(name),
  sqlc.arg(provider),
  sqlc.arg(config),
  sqlc.arg(enable)
)
RETURNING *;

-- name: GetFetchProviderByID :one
SELECT * FROM model.fetch_providers WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: GetFetchProviderByName :one
SELECT * FROM model.fetch_providers WHERE team_id = iam.memoh_current_team_id() AND name = sqlc.arg(name);

-- name: ListFetchProviders :many
SELECT * FROM model.fetch_providers
WHERE team_id = iam.memoh_current_team_id()
ORDER BY created_at DESC;

-- name: ListFetchProvidersByProvider :many
SELECT * FROM model.fetch_providers
WHERE team_id = iam.memoh_current_team_id() AND provider = sqlc.arg(provider)
ORDER BY created_at DESC;

-- name: UpdateFetchProvider :one
UPDATE model.fetch_providers
SET
  name = sqlc.arg(name),
  provider = sqlc.arg(provider),
  config = sqlc.arg(config),
  enable = sqlc.arg(enable),
  updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id)
RETURNING *;

-- name: DeleteFetchProvider :exec
DELETE FROM model.fetch_providers WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);
