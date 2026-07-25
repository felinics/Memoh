-- 0090_plugin_system
-- Remove plugin installation records and plugin-managed MCP metadata.

DROP TABLE IF EXISTS bot_plugin_resources;

DROP INDEX IF EXISTS idx_mcp_connections_plugin_installation_id;

ALTER TABLE mcp_connections
  DROP COLUMN IF EXISTS metadata,
  DROP COLUMN IF EXISTS visible,
  DROP COLUMN IF EXISTS managed_resource_key,
  DROP COLUMN IF EXISTS managed_by_plugin_installation_id;

DROP TABLE IF EXISTS bot_plugin_installations;
