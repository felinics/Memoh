-- 0106_provider_oauth_metadata
-- Remove provider OAuth metadata storage.
ALTER TABLE provider_oauth_tokens
  DROP COLUMN IF EXISTS metadata;
