-- name: CreateBot :one
INSERT INTO bots (owner_user_id, name, display_name, avatar_url, timezone, is_active, metadata, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, owner_user_id, name, display_name, avatar_url, timezone, is_active, status, language, reasoning_effort, chat_model_id, search_provider_id, memory_provider_id, metadata, created_at, updated_at;

-- name: GetBotByID :one
SELECT id, owner_user_id, name, display_name, avatar_url, timezone, is_active, status, language, reasoning_effort, chat_model_id, search_provider_id, memory_provider_id, compaction_enabled, compaction_threshold, compaction_target_percent, compaction_model_id, metadata, created_at, updated_at
FROM bots
WHERE team_id = public.memoh_current_team_id() AND id = $1;

-- name: GetBotByName :one
SELECT id, owner_user_id, name, display_name, avatar_url, timezone, is_active, status, language, reasoning_effort, chat_model_id, search_provider_id, memory_provider_id, compaction_enabled, compaction_threshold, compaction_target_percent, compaction_model_id, metadata, created_at, updated_at
FROM bots
WHERE team_id = public.memoh_current_team_id() AND name = $1;

-- name: ListBotsByOwner :many
SELECT id, owner_user_id, name, display_name, avatar_url, timezone, is_active, status, language, reasoning_effort, chat_model_id, search_provider_id, memory_provider_id, metadata, created_at, updated_at
FROM bots
WHERE team_id = public.memoh_current_team_id() AND owner_user_id = $1
ORDER BY created_at DESC;

-- name: ListAccessibleBots :many
SELECT id, owner_user_id, name, display_name, avatar_url, timezone, is_active, status, language, reasoning_effort, chat_model_id, search_provider_id, memory_provider_id, metadata, created_at, updated_at
FROM bots b
WHERE b.team_id = public.memoh_current_team_id()
  AND (
    b.owner_user_id = $1
    OR EXISTS (
      SELECT 1 FROM bot_user_grants g
      WHERE g.team_id = b.team_id
        AND g.bot_id = b.id
        AND (
          g.subject_type = 'everyone'
          OR (g.subject_type = 'user' AND g.user_id = $1)
        )
    )
  )
ORDER BY b.created_at DESC;

-- name: UpdateBotProfile :one
-- The runtime config epoch invalidates warm ACP processes, so it must move
-- only when the ACP launch configuration (the metadata 'acp' subtree) actually
-- changes. A rename, avatar, timezone, or activation toggle must not tear down
-- a mid-conversation runtime.
UPDATE bots
SET name = $2,
    display_name = $3,
    avatar_url = $4,
    timezone = $5,
    is_active = $6,
    metadata = $7,
    runtime_config_epoch = runtime_config_epoch
        + CASE
            WHEN COALESCE(metadata -> 'acp', 'null'::jsonb)
                 IS DISTINCT FROM COALESCE($7::jsonb -> 'acp', 'null'::jsonb)
            THEN 1
            ELSE 0
          END,
    updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = $1
RETURNING id, owner_user_id, name, display_name, avatar_url, timezone, is_active, status, language, reasoning_effort, chat_model_id, search_provider_id, memory_provider_id, metadata, created_at, updated_at;

-- name: BumpBotRuntimeConfigEpoch :one
UPDATE bots
SET runtime_config_epoch = runtime_config_epoch + 1,
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(bot_id)
RETURNING runtime_config_epoch;

-- name: UpdateBotOwner :one
UPDATE bots
SET owner_user_id = $2,
    updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = $1
RETURNING id, owner_user_id, name, display_name, avatar_url, timezone, is_active, status, language, reasoning_effort, chat_model_id, search_provider_id, memory_provider_id, metadata, created_at, updated_at;

-- name: UpdateBotStatus :exec
UPDATE bots
SET status = $2,
    updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = $1;

-- name: TouchBotActivity :exec
UPDATE bots
SET updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = sqlc.arg(bot_id);

-- name: ClearBotRuntimeData :exec
WITH target_sessions AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.bot_id = sqlc.arg(bot_id)
  ORDER BY session.id
  FOR UPDATE
),
target_compaction_artifacts AS MATERIALIZED (
  SELECT compact.id
  FROM bot_history_message_compacts compact
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.bot_id = sqlc.arg(bot_id)
    AND (SELECT count(*) FROM target_sessions) >= 0
  ORDER BY compact.id
  FOR UPDATE
),
deleted_compaction_artifacts AS (
  DELETE FROM bot_history_message_compacts compact
  USING target_compaction_artifacts target
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.id = target.id
  RETURNING compact.id
),
target_messages AS MATERIALIZED (
  SELECT message.id
  FROM bot_history_messages message
  WHERE message.team_id = public.memoh_current_team_id()
    AND message.bot_id = sqlc.arg(bot_id)
    AND (SELECT count(*) FROM target_sessions) >= 0
    AND (SELECT count(*) FROM deleted_compaction_artifacts) >= 0
  ORDER BY message.id
  FOR UPDATE
),
deleted_messages AS (
  DELETE FROM bot_history_messages message
  USING target_messages target
  WHERE message.team_id = public.memoh_current_team_id()
    AND message.id = target.id
  RETURNING message.id
),
deleted_sessions AS (
  DELETE FROM bot_sessions session
  USING target_sessions target
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = target.id
    AND (SELECT count(*) FROM deleted_messages) >= 0
  RETURNING session.id
)
DELETE FROM bot_channel_routes route
WHERE route.team_id = public.memoh_current_team_id()
  AND route.bot_id = sqlc.arg(bot_id)
  AND (SELECT count(*) FROM deleted_sessions) >= 0;

-- name: DeleteBotByID :exec
-- Runtime-fenced writers lock the bot parent before a child session. Keep the
-- same parent-before-child order here so deletion cannot deadlock with them.
WITH target_bot AS MATERIALIZED (
  SELECT bot.id
  FROM bots bot
  WHERE bot.team_id = public.memoh_current_team_id()
    AND bot.id = sqlc.arg(id)
  FOR UPDATE
),
target_sessions AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.bot_id = sqlc.arg(id)
    AND EXISTS (SELECT 1 FROM target_bot)
  ORDER BY session.id
  FOR UPDATE
),
target_compaction_artifacts AS MATERIALIZED (
  SELECT compact.id
  FROM bot_history_message_compacts compact
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.bot_id = sqlc.arg(id)
    AND (SELECT count(*) FROM target_sessions) >= 0
  ORDER BY compact.id
  FOR UPDATE
),
deleted_compaction_artifacts AS (
  DELETE FROM bot_history_message_compacts compact
  USING target_compaction_artifacts target
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.id = target.id
  RETURNING compact.id
),
target_messages AS MATERIALIZED (
  SELECT message.id
  FROM bot_history_messages message
  WHERE message.team_id = public.memoh_current_team_id()
    AND message.bot_id = sqlc.arg(id)
    AND (SELECT count(*) FROM target_sessions) >= 0
    AND (SELECT count(*) FROM deleted_compaction_artifacts) >= 0
  ORDER BY message.id
  FOR UPDATE
),
deleted_messages AS (
  DELETE FROM bot_history_messages message
  USING target_messages target
  WHERE message.team_id = public.memoh_current_team_id()
    AND message.id = target.id
  RETURNING message.id
),
deleted_sessions AS (
  DELETE FROM bot_sessions session
  USING target_sessions target
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = target.id
    AND (SELECT count(*) FROM deleted_messages) >= 0
  RETURNING session.id
)
DELETE FROM bots bot
USING target_bot target
WHERE bot.team_id = public.memoh_current_team_id()
  AND bot.id = target.id
  AND (SELECT count(*) FROM target_sessions) >= 0
  AND (SELECT count(*) FROM deleted_sessions) >= 0;

-- name: LockBotForSessionWrite :one
SELECT id
FROM bots
WHERE team_id = public.memoh_current_team_id()
  AND id = $1
FOR KEY SHARE;
