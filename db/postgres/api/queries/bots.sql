-- name: CreateBot :one
INSERT INTO api.bots (owner_user_id, name, display_name, avatar_url, timezone, is_active, metadata, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, owner_user_id, name, display_name, avatar_url, timezone, is_active, status, language, reasoning_enabled, reasoning_effort, chat_model_id, search_provider_id, memory_provider_id, heartbeat_enabled, heartbeat_interval, heartbeat_prompt, metadata, created_at, updated_at;

-- name: GetBotByID :one
SELECT id, owner_user_id, name, display_name, avatar_url, timezone, is_active, status, language, reasoning_enabled, reasoning_effort, chat_model_id, search_provider_id, memory_provider_id, heartbeat_enabled, heartbeat_interval, heartbeat_prompt, compaction_enabled, compaction_threshold, compaction_ratio, compaction_model_id, metadata, created_at, updated_at
FROM api.bots
WHERE team_id = iam.memoh_current_team_id() AND id = $1;

-- name: GetBotByName :one
SELECT id, owner_user_id, name, display_name, avatar_url, timezone, is_active, status, language, reasoning_enabled, reasoning_effort, chat_model_id, search_provider_id, memory_provider_id, heartbeat_enabled, heartbeat_interval, heartbeat_prompt, compaction_enabled, compaction_threshold, compaction_ratio, compaction_model_id, metadata, created_at, updated_at
FROM api.bots
WHERE team_id = iam.memoh_current_team_id() AND name = $1;

-- name: ListBotsByOwner :many
SELECT id, owner_user_id, name, display_name, avatar_url, timezone, is_active, status, language, reasoning_enabled, reasoning_effort, chat_model_id, search_provider_id, memory_provider_id, heartbeat_enabled, heartbeat_interval, heartbeat_prompt, metadata, created_at, updated_at
FROM api.bots
WHERE team_id = iam.memoh_current_team_id() AND owner_user_id = $1
ORDER BY created_at DESC;

-- name: ListAccessibleBots :many
SELECT id, owner_user_id, name, display_name, avatar_url, timezone, is_active, status, language, reasoning_enabled, reasoning_effort, chat_model_id, search_provider_id, memory_provider_id, heartbeat_enabled, heartbeat_interval, heartbeat_prompt, metadata, created_at, updated_at
FROM api.bots b
WHERE b.team_id = iam.memoh_current_team_id()
  AND (
    b.owner_user_id = $1
    OR EXISTS (
      SELECT 1 FROM api.bot_user_grants g
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
UPDATE api.bots
SET name = $2,
    display_name = $3,
    avatar_url = $4,
    timezone = $5,
    is_active = $6,
    metadata = $7,
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = $1
RETURNING id, owner_user_id, name, display_name, avatar_url, timezone, is_active, status, language, reasoning_enabled, reasoning_effort, chat_model_id, search_provider_id, memory_provider_id, heartbeat_enabled, heartbeat_interval, heartbeat_prompt, metadata, created_at, updated_at;

-- name: UpdateBotOwner :one
UPDATE api.bots
SET owner_user_id = $2,
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = $1
RETURNING id, owner_user_id, name, display_name, avatar_url, timezone, is_active, status, language, reasoning_enabled, reasoning_effort, chat_model_id, search_provider_id, memory_provider_id, heartbeat_enabled, heartbeat_interval, heartbeat_prompt, metadata, created_at, updated_at;

-- name: UpdateBotStatus :exec
UPDATE api.bots
SET status = $2,
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = $1;

-- name: DeleteBotByID :exec
-- API-owned row delete only. Agent/channel history cleanup stays on those owners.
DELETE FROM api.bots
WHERE team_id = iam.memoh_current_team_id() AND id = $1;

-- name: TouchBotActivity :exec
UPDATE api.bots
SET updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(bot_id);

-- name: LockBotForSessionWrite :one
SELECT id
FROM api.bots
WHERE team_id = iam.memoh_current_team_id()
  AND id = $1
FOR KEY SHARE;

-- name: LockBotExclusive :one
SELECT id
FROM api.bots
WHERE team_id = iam.memoh_current_team_id()
  AND id = $1
FOR UPDATE;

-- name: ListHeartbeatEnabledBots :many
SELECT id, owner_user_id, heartbeat_enabled, heartbeat_interval, heartbeat_prompt
FROM api.bots
WHERE team_id = iam.memoh_current_team_id() AND heartbeat_enabled = true AND status = 'ready';

-- name: GetBotOverlayConfig :one
SELECT
  overlay_enabled,
  overlay_provider,
  overlay_config
FROM api.bots
WHERE team_id = iam.memoh_current_team_id() AND id = $1;

-- name: DeleteSettingsByBotID :exec
UPDATE api.bots
SET language = 'auto',
    command_ui_language = 'auto',
    reasoning_enabled = false,
    reasoning_effort = 'medium',
    heartbeat_enabled = false,
    heartbeat_interval = 1440,
    heartbeat_prompt = '',
    compaction_enabled = false,
    compaction_threshold = 100000,
    compaction_ratio = 80,
    chat_model_id = NULL,
    chat_runtime = 'model',
    chat_acp_agent_id = NULL,
    chat_acp_project_path = '/data',
    chat_acp_project_mode = 'project',
    heartbeat_model_id = NULL,
    compaction_model_id = NULL,
    image_model_id = NULL,
    search_provider_id = NULL,
    fetch_provider_id = NULL,
    memory_provider_id = NULL,
    tts_model_id = NULL,
    transcription_model_id = NULL,
    video_model_id = NULL,
    persist_full_tool_results = false,
    show_tool_calls_in_im = false,
    tool_approval_config = '{"enabled":false,"read":{"require_approval":false,"bypass_globs":[],"force_review_globs":[]},"write":{"require_approval":true,"bypass_globs":["/data/**","/tmp/**"],"force_review_globs":[]},"exec":{"require_approval":false,"bypass_commands":[],"force_review_commands":[]}}'::jsonb,
    display_enabled = false,
    overlay_provider = '',
    overlay_enabled = false,
    overlay_config = '{}'::jsonb,
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = $1;
