-- name: CreateEmailProvider :one
INSERT INTO channel.email_providers (user_id, name, provider, config)
VALUES (
  sqlc.arg(user_id),
  sqlc.arg(name),
  sqlc.arg(provider),
  sqlc.arg(config)
)
RETURNING *;

-- name: GetEmailProviderByID :one
SELECT * FROM channel.email_providers WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: GetEmailProviderByIDAndUser :one
SELECT * FROM channel.email_providers
WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id);

-- name: GetEmailProviderByNameAndUser :one
SELECT * FROM channel.email_providers
WHERE team_id = iam.memoh_current_team_id() AND user_id = sqlc.arg(user_id)
  AND name = sqlc.arg(name);

-- name: ListEmailProviders :many
SELECT * FROM channel.email_providers
WHERE team_id = iam.memoh_current_team_id()
ORDER BY created_at DESC;

-- name: ListEmailProvidersByUser :many
SELECT * FROM channel.email_providers
WHERE team_id = iam.memoh_current_team_id() AND user_id = sqlc.arg(user_id)
ORDER BY created_at DESC;

-- name: ListEmailProvidersByProvider :many
SELECT * FROM channel.email_providers
WHERE team_id = iam.memoh_current_team_id() AND provider = sqlc.arg(provider)
ORDER BY created_at DESC;

-- name: ListEmailProvidersByUserAndProvider :many
SELECT * FROM channel.email_providers
WHERE team_id = iam.memoh_current_team_id() AND user_id = sqlc.arg(user_id)
  AND provider = sqlc.arg(provider)
ORDER BY created_at DESC;

-- name: UpdateEmailProvider :one
UPDATE channel.email_providers
SET
  name = sqlc.arg(name),
  provider = sqlc.arg(provider),
  config = sqlc.arg(config),
  updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id)
RETURNING *;

-- name: UpdateEmailProviderByIDAndUser :one
UPDATE channel.email_providers
SET
  name = sqlc.arg(name),
  provider = sqlc.arg(provider),
  config = sqlc.arg(config),
  updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
RETURNING *;

-- name: DeleteEmailProvider :exec
DELETE FROM channel.email_providers WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: DeleteEmailProviderByIDAndUser :exec
DELETE FROM channel.email_providers
WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id);
