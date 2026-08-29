-- name: CreateBotAgent :one
INSERT INTO bot_agents (bot_id, name, runtime, enabled, metadata)
VALUES (
  sqlc.arg(bot_id),
  sqlc.arg(name),
  sqlc.arg(runtime),
  sqlc.arg(enabled),
  sqlc.arg(metadata)
)
RETURNING *;

-- name: GetBotAgentByID :one
SELECT *
FROM bot_agents
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(id);

-- name: GetActiveBotAgentByID :one
SELECT *
FROM bot_agents
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(id)
  AND enabled
  AND deleted_at IS NULL;

-- name: LockBotForAgentMutation :one
SELECT id
FROM bots
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(bot_id)
FOR NO KEY UPDATE;

-- name: ListBotAgents :many
SELECT *
FROM bot_agents
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND deleted_at IS NULL
ORDER BY created_at, id;

-- name: FindActiveBotAgentByRuntimeProvider :one
SELECT *
FROM bot_agents
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND runtime = sqlc.arg(runtime)
  AND metadata->>'provider' = sqlc.arg(provider)::text
  AND enabled
  AND deleted_at IS NULL
ORDER BY created_at, id
LIMIT 1;

-- name: UpdateBotAgent :one
UPDATE bot_agents
SET name = sqlc.arg(name),
    enabled = sqlc.arg(enabled),
    updated_at = now()
WHERE bot_agents.team_id = public.memoh_current_team_id()
  AND bot_agents.bot_id = sqlc.arg(bot_id)
  AND bot_agents.id = sqlc.arg(id)
  AND bot_agents.deleted_at IS NULL
  AND (
    sqlc.arg(enabled)::boolean
    OR NOT EXISTS (
      SELECT 1
      FROM bots
      WHERE bots.team_id = public.memoh_current_team_id()
        AND bots.id = sqlc.arg(bot_id)
        AND bots.default_bot_agent_id = bot_agents.id
    )
  )
RETURNING *;

-- name: SoftDeleteBotAgent :one
UPDATE bot_agents
SET enabled = false,
    deleted_at = now(),
    updated_at = now()
WHERE bot_agents.team_id = public.memoh_current_team_id()
  AND bot_agents.bot_id = sqlc.arg(bot_id)
  AND bot_agents.id = sqlc.arg(id)
  AND bot_agents.deleted_at IS NULL
  AND NOT EXISTS (
    SELECT 1
    FROM bots
    WHERE bots.team_id = public.memoh_current_team_id()
      AND bots.id = sqlc.arg(bot_id)
      AND bots.default_bot_agent_id = bot_agents.id
  )
RETURNING *;

-- name: BotAgentIsDefault :one
SELECT EXISTS (
  SELECT 1
  FROM bots
  WHERE team_id = public.memoh_current_team_id()
    AND id = sqlc.arg(bot_id)
    AND default_bot_agent_id = sqlc.arg(id)
) AS is_default;
