-- name: EvaluateBotACLRule :one
-- Mode-based ACL: only rules opposite to api.bots.acl_default_effect can override the default.
-- If no matching override exists, returns api.bots.acl_default_effect.
SELECT COALESCE((
  SELECT r.effect
  FROM api.bot_acl_rules r
  WHERE r.team_id = iam.memoh_current_team_id() AND r.bot_id = b.id
    AND r.enabled = true
    AND r.action = sqlc.arg(action)
    AND r.effect <> b.acl_default_effect
    AND (r.subject_channel_type IS NULL OR r.subject_channel_type = sqlc.narg(subject_channel_type)::text)
    AND (r.channel_identity_id IS NULL OR r.channel_identity_id = sqlc.narg(channel_identity_id)::uuid)
    AND (r.source_conversation_type IS NULL OR r.source_conversation_type = sqlc.narg(source_conversation_type)::text)
    AND (r.source_conversation_id IS NULL OR r.source_conversation_id = sqlc.narg(source_conversation_id)::text)
    AND (r.source_thread_id IS NULL OR r.source_thread_id = sqlc.narg(source_thread_id)::text)
  LIMIT 1
), b.acl_default_effect) AS effect
FROM api.bots b
WHERE b.team_id = iam.memoh_current_team_id() AND b.id = sqlc.arg(bot_id);

-- name: GetBotACLDefaultEffect :one
SELECT acl_default_effect FROM api.bots WHERE team_id = iam.memoh_current_team_id() AND id = $1;

-- name: SetBotACLDefaultEffect :exec
UPDATE api.bots SET acl_default_effect = $2, updated_at = now() WHERE team_id = iam.memoh_current_team_id() AND id = $1;

-- name: ListBotACLRules :many
SELECT
  id,
  bot_id,
  enabled,
  description,
  action,
  effect,
  channel_identity_id,
  subject_channel_type,
  source_conversation_type,
  source_conversation_id,
  source_thread_id,
  created_by_user_id,
  created_at,
  updated_at
FROM api.bot_acl_rules
WHERE team_id = iam.memoh_current_team_id()
  AND bot_id = $1
  AND action = 'chat.trigger'
ORDER BY created_at DESC;

-- name: CreateBotACLRule :one
INSERT INTO api.bot_acl_rules (
  bot_id,
  enabled,
  description,
  action,
  effect,
  channel_identity_id,
  subject_channel_type,
  source_channel,
  source_conversation_type,
  source_conversation_id,
  source_thread_id,
  created_by_user_id
)
VALUES (
  $1,
  $2,
  sqlc.narg(description)::text,
  'chat.trigger',
  $3,
  sqlc.narg(channel_identity_id)::uuid,
  sqlc.narg(subject_channel_type)::text,
  sqlc.narg(source_channel)::text,
  sqlc.narg(source_conversation_type)::text,
  sqlc.narg(source_conversation_id)::text,
  sqlc.narg(source_thread_id)::text,
  $4
)
RETURNING *;

-- name: UpdateBotACLRule :one
UPDATE api.bot_acl_rules
SET
  enabled = $2,
  description = sqlc.narg(description)::text,
  effect = $3,
  channel_identity_id = sqlc.narg(channel_identity_id)::uuid,
  subject_channel_type = sqlc.narg(subject_channel_type)::text,
  source_channel = sqlc.narg(source_channel)::text,
  source_conversation_type = sqlc.narg(source_conversation_type)::text,
  source_conversation_id = sqlc.narg(source_conversation_id)::text,
  source_thread_id = sqlc.narg(source_thread_id)::text,
  updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = $1
RETURNING *;

-- name: DeleteBotACLRuleByID :exec
DELETE FROM api.bot_acl_rules WHERE team_id = iam.memoh_current_team_id() AND id = $1;
