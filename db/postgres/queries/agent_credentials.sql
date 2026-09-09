-- name: CreateAgentCredential :one
INSERT INTO agent_credentials (
    owner_user_id, provider, auth_kind, label,
    encrypted_payload, encryption_nonce, key_version,
    account_metadata, expires_at
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7,
    $8, $9
)
RETURNING *;

-- name: GetAgentCredential :one
SELECT * FROM agent_credentials
WHERE id = $1 AND team_id = public.memoh_current_team_id();

-- name: UpdateAgentCredentialPayloadCAS :one
UPDATE agent_credentials
SET encrypted_payload = $1,
    encryption_nonce = $2,
    key_version = $3,
    account_metadata = $4,
    expires_at = $5,
    credential_version = credential_version + 1,
    updated_at = now()
WHERE id = $6
  AND team_id = public.memoh_current_team_id()
  AND credential_version = sqlc.arg(expected_version)
  AND revoked_at IS NULL
RETURNING *;

-- name: RevokeAgentCredentialByID :one
UPDATE agent_credentials
SET revoked_at = now(),
    credential_version = credential_version + 1,
    updated_at = now()
WHERE id = $1
  AND team_id = public.memoh_current_team_id()
  AND revoked_at IS NULL
RETURNING *;

-- name: CountBotAgentCredentialRefs :one
SELECT count(*) FROM bot_agents
WHERE agent_credential_id = $1
  AND team_id = public.memoh_current_team_id()
  AND deleted_at IS NULL;

-- name: GetBotAgentCredential :one
SELECT c.*, a.runtime::text AS agent_runtime
FROM bot_agents a
JOIN agent_credentials c
  ON c.team_id = a.team_id AND c.id = a.agent_credential_id
WHERE a.bot_id = $1
  AND a.id = sqlc.arg(bot_agent_id)
  AND a.team_id = public.memoh_current_team_id()
  AND a.deleted_at IS NULL;

-- name: SetBotAgentCredential :one
UPDATE bot_agents
SET agent_credential_id = sqlc.arg(credential_id),
    updated_at = now()
WHERE bot_agents.bot_id = $1
  AND bot_agents.id = sqlc.arg(bot_agent_id)
  AND bot_agents.team_id = public.memoh_current_team_id()
  AND bot_agents.deleted_at IS NULL
RETURNING (
    SELECT prev.agent_credential_id
    FROM bot_agents prev
    WHERE prev.id = bot_agents.id
) AS previous_credential_id;

-- name: ClearBotAgentCredential :one
UPDATE bot_agents
SET agent_credential_id = NULL,
    updated_at = now()
WHERE bot_agents.bot_id = $1
  AND bot_agents.id = sqlc.arg(bot_agent_id)
  AND bot_agents.team_id = public.memoh_current_team_id()
  AND bot_agents.deleted_at IS NULL
  AND bot_agents.agent_credential_id IS NOT NULL
RETURNING (
    SELECT prev.agent_credential_id
    FROM bot_agents prev
    WHERE prev.id = bot_agents.id
) AS previous_credential_id;

-- name: GetBotAgentRuntime :one
SELECT runtime
FROM bot_agents
WHERE bot_id = $1
  AND id = sqlc.arg(bot_agent_id)
  AND team_id = public.memoh_current_team_id()
  AND deleted_at IS NULL;

-- name: RevokeAgentCredentialsForBot :exec
UPDATE agent_credentials c
SET revoked_at = now(),
    credential_version = credential_version + 1,
    updated_at = now()
WHERE c.team_id = public.memoh_current_team_id()
  AND c.revoked_at IS NULL
  AND EXISTS (
    SELECT 1 FROM bot_agents a
    WHERE a.team_id = c.team_id AND a.agent_credential_id = c.id AND a.bot_id = $1
  )
  AND NOT EXISTS (
    SELECT 1 FROM bot_agents o
    WHERE o.team_id = c.team_id AND o.agent_credential_id = c.id
      AND o.bot_id <> $1 AND o.deleted_at IS NULL
  );
