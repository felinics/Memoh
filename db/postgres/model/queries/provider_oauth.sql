-- name: UpsertProviderOAuthToken :one
INSERT INTO model.provider_oauth_tokens (
  provider_id,
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
  sqlc.arg(access_token),
  sqlc.arg(refresh_token),
  sqlc.arg(expires_at),
  sqlc.arg(scope),
  sqlc.arg(token_type),
  sqlc.arg(state),
  sqlc.arg(pkce_code_verifier),
  sqlc.arg(metadata)
)
ON CONFLICT (team_id, provider_id) DO UPDATE SET
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

-- name: GetProviderOAuthTokenByProvider :one
SELECT * FROM model.provider_oauth_tokens WHERE team_id = iam.memoh_current_team_id() AND provider_id = sqlc.arg(provider_id);

-- name: GetProviderOAuthTokenByState :one
SELECT * FROM model.provider_oauth_tokens WHERE team_id = iam.memoh_current_team_id() AND state = sqlc.arg(state) AND state != '';

-- name: UpdateProviderOAuthState :exec
INSERT INTO model.provider_oauth_tokens (provider_id, state, pkce_code_verifier, metadata)
VALUES (
  sqlc.arg(provider_id),
  sqlc.arg(state),
  sqlc.arg(pkce_code_verifier),
  sqlc.arg(metadata)
)
ON CONFLICT (team_id, provider_id) DO UPDATE SET
  state = EXCLUDED.state,
  pkce_code_verifier = EXCLUDED.pkce_code_verifier,
  metadata = EXCLUDED.metadata,
  updated_at = now();

-- name: DeleteProviderOAuthToken :exec
DELETE FROM model.provider_oauth_tokens WHERE team_id = iam.memoh_current_team_id() AND provider_id = sqlc.arg(provider_id);
