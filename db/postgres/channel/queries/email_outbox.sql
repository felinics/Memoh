-- name: CreateEmailOutbox :one
INSERT INTO channel.email_outbox (provider_id, bot_id, from_address, to_addresses, subject, body_text, body_html, attachments, status)
VALUES (
  sqlc.arg(provider_id),
  sqlc.arg(bot_id),
  sqlc.arg(from_address),
  sqlc.arg(to_addresses),
  sqlc.arg(subject),
  sqlc.arg(body_text),
  sqlc.arg(body_html),
  sqlc.arg(attachments),
  sqlc.arg(status)
)
RETURNING *;

-- name: GetEmailOutboxByID :one
SELECT * FROM channel.email_outbox WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: ListEmailOutboxByBot :many
SELECT * FROM channel.email_outbox
WHERE team_id = iam.memoh_current_team_id() AND bot_id = sqlc.arg(bot_id)
ORDER BY created_at DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: CountEmailOutboxByBot :one
SELECT count(*) FROM channel.email_outbox
WHERE team_id = iam.memoh_current_team_id() AND bot_id = sqlc.arg(bot_id);

-- name: UpdateEmailOutboxSent :exec
UPDATE channel.email_outbox
SET message_id = sqlc.arg(message_id), status = 'sent', sent_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: UpdateEmailOutboxFailed :exec
UPDATE channel.email_outbox
SET status = 'failed', error = sqlc.arg(error)
WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id);
