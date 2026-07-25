-- name: GetMCPOAuthToken :one
SELECT id, connection_id, resource_metadata_url, authorization_server_url,
       authorization_endpoint, token_endpoint, registration_endpoint,
       scopes_supported, client_id, client_secret, access_token, refresh_token,
       token_type, expires_at, scope, pkce_code_verifier, state_param,
       resource_uri, redirect_uri, created_at, updated_at, team_id
FROM agent.mcp_oauth_tokens
WHERE team_id = iam.memoh_current_team_id() AND connection_id = $1
LIMIT 1;

-- name: GetMCPOAuthTokenByState :one
SELECT id, connection_id, resource_metadata_url, authorization_server_url,
       authorization_endpoint, token_endpoint, registration_endpoint,
       scopes_supported, client_id, client_secret, access_token, refresh_token,
       token_type, expires_at, scope, pkce_code_verifier, state_param,
       resource_uri, redirect_uri, created_at, updated_at, team_id
FROM agent.mcp_oauth_tokens
WHERE team_id = iam.memoh_current_team_id() AND state_param = $1
LIMIT 1;

-- name: UpsertMCPOAuthDiscovery :one
INSERT INTO agent.mcp_oauth_tokens (connection_id, resource_metadata_url, authorization_server_url,
    authorization_endpoint, token_endpoint, registration_endpoint, scopes_supported,
    resource_uri)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (team_id, connection_id)
DO UPDATE SET resource_metadata_url = EXCLUDED.resource_metadata_url,
              authorization_server_url = EXCLUDED.authorization_server_url,
              authorization_endpoint = EXCLUDED.authorization_endpoint,
              token_endpoint = EXCLUDED.token_endpoint,
              registration_endpoint = EXCLUDED.registration_endpoint,
              scopes_supported = EXCLUDED.scopes_supported,
              resource_uri = EXCLUDED.resource_uri,
              updated_at = now()
RETURNING id, connection_id, resource_metadata_url, authorization_server_url,
          authorization_endpoint, token_endpoint, registration_endpoint,
          scopes_supported, client_id, client_secret, access_token, refresh_token,
          token_type, expires_at, scope, pkce_code_verifier, state_param,
          resource_uri, redirect_uri, created_at, updated_at, team_id;

-- name: UpdateMCPOAuthPKCEState :exec
UPDATE agent.mcp_oauth_tokens
SET pkce_code_verifier = $2,
    state_param = $3,
    client_id = $4,
    redirect_uri = $5,
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND connection_id = $1;

-- name: UpdateMCPOAuthTokens :exec
UPDATE agent.mcp_oauth_tokens
SET access_token = $2,
    refresh_token = $3,
    token_type = $4,
    expires_at = $5,
    scope = $6,
    pkce_code_verifier = '',
    state_param = '',
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND connection_id = $1;

-- name: ClearMCPOAuthTokens :exec
UPDATE agent.mcp_oauth_tokens
SET access_token = '',
    refresh_token = '',
    expires_at = NULL,
    scope = '',
    pkce_code_verifier = '',
    state_param = '',
    redirect_uri = '',
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND connection_id = $1;

-- name: UpdateMCPOAuthClientSecret :exec
UPDATE agent.mcp_oauth_tokens
SET client_secret = $2,
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND connection_id = $1;

-- name: DeleteMCPOAuthToken :exec
DELETE FROM agent.mcp_oauth_tokens
WHERE team_id = iam.memoh_current_team_id() AND connection_id = $1;
