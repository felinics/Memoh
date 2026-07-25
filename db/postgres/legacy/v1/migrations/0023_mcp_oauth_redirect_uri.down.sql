-- 0023_mcp_oauth_redirect_uri
-- Remove redirect_uri column from mcp_oauth_tokens

ALTER TABLE mcp_oauth_tokens DROP COLUMN IF EXISTS redirect_uri;
