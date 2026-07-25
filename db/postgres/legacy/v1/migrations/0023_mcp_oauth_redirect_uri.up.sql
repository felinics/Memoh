-- 0023_mcp_oauth_redirect_uri
-- Add redirect_uri column to mcp_oauth_tokens for per-flow callback URL storage

ALTER TABLE mcp_oauth_tokens ADD COLUMN IF NOT EXISTS redirect_uri TEXT NOT NULL DEFAULT '';
