-- name: GetBotWorkspaceResourceLimits :one
SELECT * FROM runtime.bot_workspace_resource_limits WHERE team_id = iam.memoh_current_team_id() AND bot_id = sqlc.arg(bot_id);

-- name: UpsertBotWorkspaceResourceLimits :one
INSERT INTO runtime.bot_workspace_resource_limits (
  bot_id, cpu_millicores, memory_bytes, storage_bytes
)
VALUES (
  sqlc.arg(bot_id),
  sqlc.arg(cpu_millicores),
  sqlc.arg(memory_bytes),
  sqlc.arg(storage_bytes)
)
ON CONFLICT (team_id, bot_id) DO UPDATE SET
  cpu_millicores = EXCLUDED.cpu_millicores,
  memory_bytes = EXCLUDED.memory_bytes,
  storage_bytes = EXCLUDED.storage_bytes,
  updated_at = now()
RETURNING *;
