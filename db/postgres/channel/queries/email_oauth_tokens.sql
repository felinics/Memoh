-- name: UpsertEmailOAuthToken :one
INSERT INTO channel.email_oauth_tokens (email_provider_id, email_address, access_token, refresh_token, expires_at, scope, state)
VALUES (sqlc.arg(email_provider_id), sqlc.arg(email_address), sqlc.arg(access_token), sqlc.arg(refresh_token), sqlc.arg(expires_at), sqlc.arg(scope), sqlc.arg(state))
ON CONFLICT (team_id, email_provider_id) DO UPDATE SET
  email_address  = EXCLUDED.email_address,
  access_token   = EXCLUDED.access_token,
  refresh_token  = EXCLUDED.refresh_token,
  expires_at     = EXCLUDED.expires_at,
  scope          = EXCLUDED.scope,
  state          = EXCLUDED.state,
  updated_at     = now()
RETURNING *;

-- name: GetEmailOAuthTokenByProvider :one
SELECT * FROM channel.email_oauth_tokens WHERE team_id = iam.memoh_current_team_id() AND email_provider_id = sqlc.arg(email_provider_id);

-- name: GetEmailOAuthTokenByState :one
SELECT * FROM channel.email_oauth_tokens WHERE team_id = iam.memoh_current_team_id() AND state = sqlc.arg(state) AND state != '';

-- name: UpdateEmailOAuthState :exec
INSERT INTO channel.email_oauth_tokens (email_provider_id, state)
VALUES (sqlc.arg(email_provider_id), sqlc.arg(state))
ON CONFLICT (team_id, email_provider_id) DO UPDATE SET
  state      = EXCLUDED.state,
  updated_at = now();

-- name: DeleteEmailOAuthToken :exec
DELETE FROM channel.email_oauth_tokens WHERE team_id = iam.memoh_current_team_id() AND email_provider_id = sqlc.arg(email_provider_id);
