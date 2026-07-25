-- 0022_mcp_probe_and_oauth (rollback)
-- Remove probe status fields and auth_type from mcp_connections; drop mcp_oauth_tokens table

DROP INDEX IF EXISTS idx_mcp_oauth_tokens_connection_id;
DROP TABLE IF EXISTS mcp_oauth_tokens;

ALTER TABLE mcp_connections
  DROP COLUMN IF EXISTS status,
  DROP COLUMN IF EXISTS tools_cache,
  DROP COLUMN IF EXISTS last_probed_at,
  DROP COLUMN IF EXISTS status_message,
  DROP COLUMN IF EXISTS auth_type;
