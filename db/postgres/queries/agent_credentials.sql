-- name: CreateAgentCredential :one
INSERT INTO agent_credentials (
  owner_user_id, provider, auth_kind, label, encrypted_payload,
  encryption_nonce, key_version, account_metadata, expires_at
)
VALUES (
  sqlc.arg(owner_user_id), sqlc.arg(provider), sqlc.arg(auth_kind), sqlc.arg(label),
  sqlc.arg(encrypted_payload), sqlc.arg(encryption_nonce), sqlc.arg(key_version),
  sqlc.arg(account_metadata), sqlc.narg(expires_at)
)
RETURNING *;

-- name: GetAgentCredential :one
SELECT * FROM agent_credentials
WHERE team_id = public.memoh_current_team_id() AND id = sqlc.arg(id);

-- name: ListAgentCredentialsByOwner :many
SELECT * FROM agent_credentials
WHERE team_id = public.memoh_current_team_id()
  AND owner_user_id = sqlc.arg(owner_user_id)
ORDER BY created_at DESC, id DESC;

-- name: UpdateAgentCredentialLabel :one
UPDATE agent_credentials
SET label = sqlc.arg(label), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND owner_user_id = sqlc.arg(owner_user_id)
RETURNING *;

-- name: RevokeAgentCredential :one
UPDATE agent_credentials
SET revoked_at = COALESCE(revoked_at, now()),
    credential_version = credential_version + 1,
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND owner_user_id = sqlc.arg(owner_user_id)
RETURNING *;

-- name: UpdateAgentCredentialPayloadCAS :one
UPDATE agent_credentials
SET encrypted_payload = sqlc.arg(encrypted_payload),
    encryption_nonce = sqlc.arg(encryption_nonce),
    key_version = sqlc.arg(key_version),
    account_metadata = sqlc.arg(account_metadata),
    expires_at = sqlc.narg(expires_at),
    credential_version = credential_version + 1,
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND credential_version = sqlc.arg(expected_version)
  AND revoked_at IS NULL
RETURNING *;

-- name: BindBotAgentCredential :one
INSERT INTO bot_agent_credentials (bot_id, agent_id, credential_id, is_default)
VALUES (sqlc.arg(bot_id), sqlc.arg(agent_id), sqlc.arg(credential_id), false)
ON CONFLICT (team_id, bot_id, agent_id, credential_id) DO UPDATE SET updated_at = now()
RETURNING *;

-- name: ClearBotAgentCredentialDefault :exec
UPDATE bot_agent_credentials
SET is_default = false, updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND agent_id = sqlc.arg(agent_id)
  AND is_default;

-- name: SetBotAgentCredentialDefault :one
UPDATE bot_agent_credentials
SET is_default = true, updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND agent_id = sqlc.arg(agent_id)
  AND credential_id = sqlc.arg(credential_id)
RETURNING *;

-- name: UnbindBotAgentCredential :execrows
DELETE FROM bot_agent_credentials
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND agent_id = sqlc.arg(agent_id)
  AND credential_id = sqlc.arg(credential_id);

-- name: ListBotAgentCredentials :many
SELECT c.*, b.is_default, b.created_at AS binding_created_at, b.updated_at AS binding_updated_at
FROM bot_agent_credentials b
JOIN agent_credentials c ON c.team_id = b.team_id AND c.id = b.credential_id
WHERE b.team_id = public.memoh_current_team_id()
  AND b.bot_id = sqlc.arg(bot_id)
  AND b.agent_id = sqlc.arg(agent_id)
ORDER BY b.is_default DESC, b.created_at ASC, c.id ASC;

-- name: GetBotAgentCredential :one
SELECT c.*, b.is_default, b.created_at AS binding_created_at, b.updated_at AS binding_updated_at
FROM bot_agent_credentials b
JOIN agent_credentials c ON c.team_id = b.team_id AND c.id = b.credential_id
WHERE b.team_id = public.memoh_current_team_id()
  AND b.bot_id = sqlc.arg(bot_id)
  AND b.agent_id = sqlc.arg(agent_id)
  AND b.credential_id = sqlc.arg(credential_id);

-- name: GetDefaultBotAgentCredential :one
SELECT c.*, b.is_default, b.created_at AS binding_created_at, b.updated_at AS binding_updated_at
FROM bot_agent_credentials b
JOIN agent_credentials c ON c.team_id = b.team_id AND c.id = b.credential_id
WHERE b.team_id = public.memoh_current_team_id()
  AND b.bot_id = sqlc.arg(bot_id)
  AND b.agent_id = sqlc.arg(agent_id)
  AND b.is_default;

-- name: ListAgentCredentialBindings :many
SELECT bot_id, agent_id
FROM bot_agent_credentials
WHERE team_id = public.memoh_current_team_id()
  AND credential_id = sqlc.arg(credential_id)
ORDER BY bot_id, agent_id;
