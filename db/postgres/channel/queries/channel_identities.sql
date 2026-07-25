-- name: CreateChannelIdentity :one
INSERT INTO channel.channel_identities (channel_type, channel_subject_id, display_name, avatar_url, metadata)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, channel_type, channel_subject_id, display_name, avatar_url, metadata, created_at, updated_at, team_id;

-- name: GetChannelIdentityByID :one
SELECT id, channel_type, channel_subject_id, display_name, avatar_url, metadata, created_at, updated_at, team_id
FROM channel.channel_identities
WHERE team_id = iam.memoh_current_team_id() AND id = $1;

-- name: ListChannelIdentitiesByIDs :many
SELECT id, channel_type, channel_subject_id, display_name, avatar_url, metadata, created_at, updated_at, team_id
FROM channel.channel_identities
WHERE team_id = iam.memoh_current_team_id()
  AND id = ANY(sqlc.arg(ids)::uuid[])
ORDER BY array_position(sqlc.arg(ids)::uuid[], id);

-- name: GetChannelIdentityByIDForUpdate :one
SELECT id, channel_type, channel_subject_id, display_name, avatar_url, metadata, created_at, updated_at, team_id
FROM channel.channel_identities
WHERE team_id = iam.memoh_current_team_id() AND id = $1
FOR UPDATE;

-- name: GetChannelIdentityByChannelSubject :one
SELECT id, channel_type, channel_subject_id, display_name, avatar_url, metadata, created_at, updated_at, team_id
FROM channel.channel_identities
WHERE team_id = iam.memoh_current_team_id() AND channel_type = $1 AND channel_subject_id = $2;

-- name: UpsertChannelIdentityByChannelSubject :one
INSERT INTO channel.channel_identities (channel_type, channel_subject_id, display_name, avatar_url, metadata)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (team_id, channel_type, channel_subject_id)
DO UPDATE SET
  display_name = COALESCE(NULLIF(EXCLUDED.display_name, ''), channel.channel_identities.display_name),
  avatar_url = COALESCE(NULLIF(EXCLUDED.avatar_url, ''), channel.channel_identities.avatar_url),
  metadata = EXCLUDED.metadata,
  updated_at = now()
RETURNING id, channel_type, channel_subject_id, display_name, avatar_url, metadata, created_at, updated_at, team_id;

-- name: SearchChannelIdentities :many
SELECT
  ci.id,
  ci.channel_type,
  ci.channel_subject_id,
  ci.display_name,
  ci.avatar_url,
  ci.metadata,
  ci.created_at,
  ci.updated_at,
  ci.team_id
FROM channel.channel_identities ci
WHERE
  ci.team_id = iam.memoh_current_team_id()
  AND (
    sqlc.arg(query)::text = ''
    OR ci.channel_type ILIKE '%' || sqlc.arg(query)::text || '%'
    OR ci.channel_subject_id ILIKE '%' || sqlc.arg(query)::text || '%'
    OR COALESCE(ci.display_name, '') ILIKE '%' || sqlc.arg(query)::text || '%'
  )
ORDER BY ci.updated_at DESC
LIMIT sqlc.arg(limit_count);
