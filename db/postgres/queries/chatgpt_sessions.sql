-- name: EnsureChatGPTRuntimeHost :one
INSERT INTO chatgpt_runtime_hosts DEFAULT VALUES
ON CONFLICT (team_id) DO UPDATE SET team_id = EXCLUDED.team_id
RETURNING host_id;

-- name: EnsureChatGPTProviderSession :exec
INSERT INTO chatgpt_provider_sessions (provider_id, owner_user_id)
VALUES (sqlc.arg(provider_id), sqlc.arg(owner_user_id))
ON CONFLICT (team_id, provider_id) DO NOTHING;

-- name: GetChatGPTProviderSession :one
SELECT * FROM chatgpt_provider_sessions
WHERE team_id = public.memoh_current_team_id() AND provider_id = sqlc.arg(provider_id);

-- name: LockChatGPTProviderSession :one
SELECT * FROM chatgpt_provider_sessions
WHERE team_id = public.memoh_current_team_id() AND provider_id = sqlc.arg(provider_id)
FOR UPDATE;

-- name: SaveChatGPTProviderSession :exec
UPDATE chatgpt_provider_sessions SET
    encrypted_payload = sqlc.arg(encrypted_payload), encryption_nonce = sqlc.arg(encryption_nonce), updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND provider_id = sqlc.arg(provider_id);
