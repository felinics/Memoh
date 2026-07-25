-- 0106_provider_oauth_metadata
-- Persist short-lived device authorization state and token-derived account metadata.
ALTER TABLE provider_oauth_tokens
  ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb;
