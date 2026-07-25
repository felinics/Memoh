-- 0028_email_oauth_tokens
-- Store OAuth2 tokens for Gmail email providers.

CREATE TABLE IF NOT EXISTS email_oauth_tokens (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email_provider_id UUID NOT NULL UNIQUE REFERENCES email_providers(id) ON DELETE CASCADE,
  email_address TEXT NOT NULL DEFAULT '',
  access_token TEXT NOT NULL DEFAULT '',
  refresh_token TEXT NOT NULL DEFAULT '',
  expires_at TIMESTAMPTZ,
  scope TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_email_oauth_tokens_state ON email_oauth_tokens(state) WHERE state != '';
