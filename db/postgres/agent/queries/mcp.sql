-- name: GetMCPConnectionByID :one
SELECT id, bot_id, name, type, config, is_active, status, tools_cache, last_probed_at, status_message, auth_type,
       managed_by_plugin_installation_id, managed_resource_key, visible, metadata, created_at, updated_at, team_id
FROM agent.mcp_connections
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND id = $2
LIMIT 1;

-- name: ListMCPConnectionsByBotID :many
SELECT id, bot_id, name, type, config, is_active, status, tools_cache, last_probed_at, status_message, auth_type,
       managed_by_plugin_installation_id, managed_resource_key, visible, metadata, created_at, updated_at, team_id
FROM agent.mcp_connections
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1
ORDER BY created_at DESC;

-- name: CreateMCPConnection :one
INSERT INTO agent.mcp_connections (bot_id, name, type, config, is_active, auth_type)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, bot_id, name, type, config, is_active, status, tools_cache, last_probed_at, status_message, auth_type,
          managed_by_plugin_installation_id, managed_resource_key, visible, metadata, created_at, updated_at, team_id;

-- name: CreateManagedMCPConnection :one
INSERT INTO agent.mcp_connections (
  bot_id, name, type, config, is_active, auth_type,
  managed_by_plugin_installation_id, managed_resource_key, visible, metadata
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, bot_id, name, type, config, is_active, status, tools_cache, last_probed_at, status_message, auth_type,
          managed_by_plugin_installation_id, managed_resource_key, visible, metadata, created_at, updated_at, team_id;

-- name: UpdateMCPConnection :one
UPDATE agent.mcp_connections
SET name = $3,
    type = $4,
    config = $5,
    is_active = $6,
    auth_type = $7,
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND id = $2
RETURNING id, bot_id, name, type, config, is_active, status, tools_cache, last_probed_at, status_message, auth_type,
          managed_by_plugin_installation_id, managed_resource_key, visible, metadata, created_at, updated_at, team_id;

-- name: UpdateMCPConnectionActive :exec
UPDATE agent.mcp_connections
SET is_active = $3,
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND id = $2;

-- name: UpdateMCPConnectionsActiveByPlugin :exec
UPDATE agent.mcp_connections
SET is_active = $3,
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND managed_by_plugin_installation_id = $2;

-- name: UpdateMCPConnectionProbeResult :exec
UPDATE agent.mcp_connections
SET status = $3,
    tools_cache = $4,
    last_probed_at = now(),
    status_message = $5,
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND id = $2;

-- name: UpdateMCPConnectionAuthType :exec
UPDATE agent.mcp_connections
SET auth_type = $2,
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = $1;

-- name: DeleteMCPConnection :exec
DELETE FROM agent.mcp_connections
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND id = $2;

-- name: DeleteMCPConnectionsByPlugin :exec
DELETE FROM agent.mcp_connections
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND managed_by_plugin_installation_id = $2;

-- name: UpsertMCPConnectionByName :one
INSERT INTO agent.mcp_connections (bot_id, name, type, config)
VALUES ($1, $2, $3, $4)
ON CONFLICT (team_id, bot_id, name)
DO UPDATE SET type = EXCLUDED.type,
              config = EXCLUDED.config,
              updated_at = now()
RETURNING id, bot_id, name, type, config, is_active, status, tools_cache, last_probed_at, status_message, auth_type,
          managed_by_plugin_installation_id, managed_resource_key, visible, metadata, created_at, updated_at, team_id;
