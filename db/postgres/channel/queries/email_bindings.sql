-- name: CreateBotEmailBinding :one
INSERT INTO channel.bot_email_bindings (bot_id, email_provider_id, email_address, can_read, can_write, can_delete, config)
VALUES (
  sqlc.arg(bot_id),
  sqlc.arg(email_provider_id),
  sqlc.arg(email_address),
  sqlc.arg(can_read),
  sqlc.arg(can_write),
  sqlc.arg(can_delete),
  sqlc.arg(config)
)
RETURNING *;

-- name: GetBotEmailBindingByID :one
SELECT * FROM channel.bot_email_bindings WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: GetBotEmailBindingByBotAndProvider :one
SELECT * FROM channel.bot_email_bindings
WHERE team_id = iam.memoh_current_team_id() AND bot_id = sqlc.arg(bot_id) AND email_provider_id = sqlc.arg(email_provider_id);

-- name: ListBotEmailBindings :many
SELECT * FROM channel.bot_email_bindings
WHERE team_id = iam.memoh_current_team_id() AND bot_id = sqlc.arg(bot_id)
ORDER BY created_at DESC;

-- name: ListBotEmailBindingsByProvider :many
SELECT * FROM channel.bot_email_bindings
WHERE team_id = iam.memoh_current_team_id() AND email_provider_id = sqlc.arg(email_provider_id)
ORDER BY created_at DESC;

-- name: ListReadableBindingsByProvider :many
SELECT * FROM channel.bot_email_bindings
WHERE team_id = iam.memoh_current_team_id() AND email_provider_id = sqlc.arg(email_provider_id) AND can_read = TRUE
ORDER BY created_at DESC;

-- name: UpdateBotEmailBinding :one
UPDATE channel.bot_email_bindings
SET
  email_address = sqlc.arg(email_address),
  can_read = sqlc.arg(can_read),
  can_write = sqlc.arg(can_write),
  can_delete = sqlc.arg(can_delete),
  config = sqlc.arg(config),
  updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id)
RETURNING *;

-- name: DeleteBotEmailBinding :exec
DELETE FROM channel.bot_email_bindings WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);
