-- name: ListBotPeerGrants :many
SELECT
  g.id,
  g.bot_id,
  g.subject_type,
  g.subject_bot_id,
  g.permissions,
  g.created_by_user_id,
  g.created_at,
  g.updated_at,
  s.name AS subject_bot_name,
  s.display_name AS subject_bot_display_name,
  s.avatar_url AS subject_bot_avatar_url
FROM bot_peer_grants g
LEFT JOIN bots s
  ON s.team_id = g.team_id
 AND s.id = g.subject_bot_id
WHERE g.team_id = public.memoh_current_team_id() AND g.bot_id = $1
ORDER BY g.subject_type DESC, g.created_at ASC;

-- name: GetBotPeerGrantByID :one
SELECT id, bot_id, subject_type, subject_bot_id, permissions, created_by_user_id, created_at, updated_at, team_id
FROM bot_peer_grants
WHERE team_id = public.memoh_current_team_id() AND id = $1;

-- name: ListBotPeerGrantsForCaller :many
-- Callee-side resolution: every grant on bot $1 that applies to caller bot $2.
-- The any_bot row never applies to the callee itself, so a bot can never reach
-- itself through a blanket grant.
SELECT id, bot_id, subject_type, subject_bot_id, permissions
FROM bot_peer_grants
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND (
    (subject_type = 'any_bot' AND bot_id <> sqlc.arg(caller_bot_id)::uuid)
    OR (subject_type = 'bot' AND subject_bot_id = sqlc.arg(caller_bot_id)::uuid)
  );

-- name: ListBotPeerGrantsForSubject :many
-- Caller-side reverse lookup: every bot that caller $1 may reach, with the
-- callee's profile for display. Uses idx_bot_peer_grants_subject for the
-- explicit edges and a sequential scan of the (small) any_bot set.
SELECT
  g.id,
  g.bot_id,
  g.subject_type,
  g.subject_bot_id,
  g.permissions,
  b.name AS bot_name,
  b.display_name AS bot_display_name,
  b.avatar_url AS bot_avatar_url
FROM bot_peer_grants g
INNER JOIN bots b
  ON b.team_id = g.team_id
 AND b.id = g.bot_id
WHERE g.team_id = public.memoh_current_team_id()
  AND (
    (g.subject_type = 'any_bot' AND g.bot_id <> sqlc.arg(caller_bot_id)::uuid)
    OR (g.subject_type = 'bot' AND g.subject_bot_id = sqlc.arg(caller_bot_id)::uuid)
  )
ORDER BY b.created_at ASC;

-- name: CreateBotPeerGrant :one
INSERT INTO bot_peer_grants (bot_id, subject_type, subject_bot_id, permissions, created_by_user_id)
VALUES (
  $1,
  $2,
  sqlc.narg(subject_bot_id)::uuid,
  $3,
  sqlc.narg(created_by_user_id)::uuid
)
RETURNING id, bot_id, subject_type, subject_bot_id, permissions, created_by_user_id, created_at, updated_at, team_id;

-- name: UpdateBotPeerGrantPermissions :one
UPDATE bot_peer_grants
SET permissions = $2,
    updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = $1
RETURNING id, bot_id, subject_type, subject_bot_id, permissions, created_by_user_id, created_at, updated_at, team_id;

-- name: DeleteBotPeerGrantByID :exec
DELETE FROM bot_peer_grants WHERE team_id = public.memoh_current_team_id() AND id = $1;
