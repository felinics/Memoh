-- name: UpsertUserProviderOAuthToken :one
INSERT INTO model.user_provider_oauth_tokens (
  provider_id,
  user_id,
  access_token,
  refresh_token,
  expires_at,
  scope,
  token_type,
  state,
  pkce_code_verifier,
  metadata
)
VALUES (
  sqlc.arg(provider_id),
  sqlc.arg(user_id),
  sqlc.arg(access_token),
  sqlc.arg(refresh_token),
  sqlc.arg(expires_at),
  sqlc.arg(scope),
  sqlc.arg(token_type),
  sqlc.arg(state),
  sqlc.arg(pkce_code_verifier),
  sqlc.arg(metadata)
)
ON CONFLICT (team_id, provider_id, user_id) DO UPDATE SET
  access_token = EXCLUDED.access_token,
  refresh_token = EXCLUDED.refresh_token,
  expires_at = EXCLUDED.expires_at,
  scope = EXCLUDED.scope,
  token_type = EXCLUDED.token_type,
  state = EXCLUDED.state,
  pkce_code_verifier = EXCLUDED.pkce_code_verifier,
  metadata = EXCLUDED.metadata,
  updated_at = now()
RETURNING *;

-- name: GetUserProviderOAuthToken :one
SELECT * FROM model.user_provider_oauth_tokens
WHERE team_id = iam.memoh_current_team_id() AND provider_id = sqlc.arg(provider_id)
  AND user_id = sqlc.arg(user_id);

-- name: GetUserProviderOAuthTokenByState :one
SELECT * FROM model.user_provider_oauth_tokens
WHERE team_id = iam.memoh_current_team_id() AND state = sqlc.arg(state)
  AND state != '';

-- name: UpdateUserProviderOAuthState :exec
INSERT INTO model.user_provider_oauth_tokens (provider_id, user_id, state, pkce_code_verifier, metadata)
VALUES (
  sqlc.arg(provider_id),
  sqlc.arg(user_id),
  sqlc.arg(state),
  sqlc.arg(pkce_code_verifier),
  sqlc.arg(metadata)
)
ON CONFLICT (team_id, provider_id, user_id) DO UPDATE SET
  state = EXCLUDED.state,
  pkce_code_verifier = EXCLUDED.pkce_code_verifier,
  metadata = EXCLUDED.metadata,
  updated_at = now();

-- name: DeleteUserProviderOAuthToken :exec
DELETE FROM model.user_provider_oauth_tokens
WHERE team_id = iam.memoh_current_team_id() AND provider_id = sqlc.arg(provider_id)
  AND user_id = sqlc.arg(user_id);
