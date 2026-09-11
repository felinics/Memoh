-- name: CreateAgentAuthorization :one
INSERT INTO agent_authorizations (owner_user_id, runtime, auth_kind, status, encrypted_payload, encryption_nonce, expires_at, poll_after)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING *;

-- name: LockAgentAuthorizationOwner :exec
SELECT pg_advisory_xact_lock(hashtextextended(public.memoh_current_team_id()::text || sqlc.arg(owner_user_id)::text, 0));

-- name: GetAgentAuthorization :one
SELECT * FROM agent_authorizations
WHERE id = $1 AND owner_user_id = $2 AND team_id = public.memoh_current_team_id()
  AND expires_at > now()
FOR UPDATE;

-- name: UpdateAgentAuthorization :one
UPDATE agent_authorizations SET status = $2, encrypted_payload = $3, encryption_nonce = $4,
    expires_at = $5, poll_after = $6, claimed_agent_id = $7
WHERE id = $1 AND team_id = public.memoh_current_team_id() RETURNING *;

-- name: DeleteAgentAuthorization :exec
DELETE FROM agent_authorizations
WHERE id = $1 AND owner_user_id = $2 AND team_id = public.memoh_current_team_id();

-- name: PruneAgentAuthorizations :exec
DELETE FROM agent_authorizations WHERE team_id = public.memoh_current_team_id() AND expires_at <= now();

-- name: CountAgentAuthorizations :one
SELECT count(*) FROM agent_authorizations
WHERE owner_user_id = $1 AND team_id = public.memoh_current_team_id() AND status <> 'claimed' AND expires_at > now();
