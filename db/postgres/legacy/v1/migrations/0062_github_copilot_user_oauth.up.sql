-- 0062_github_copilot_user_oauth
-- Add github-copilot as a provider client type and store OAuth tokens per user.

ALTER TABLE IF EXISTS providers DROP CONSTRAINT IF EXISTS providers_client_type_check;

ALTER TABLE IF EXISTS providers
  ADD CONSTRAINT providers_client_type_check CHECK (
    client_type IN (
      'openai-responses',
      'openai-completions',
      'anthropic-messages',
      'google-generative-ai',
      'openai-codex',
      'github-copilot',
      'edge-speech'
    )
  );

CREATE TABLE IF NOT EXISTS user_provider_oauth_tokens (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_id UUID NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  access_token TEXT NOT NULL DEFAULT '',
  refresh_token TEXT NOT NULL DEFAULT '',
  expires_at TIMESTAMPTZ,
  scope TEXT NOT NULL DEFAULT '',
  token_type TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT '',
  pkce_code_verifier TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT user_provider_oauth_tokens_provider_user_unique UNIQUE (provider_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_user_provider_oauth_tokens_state
  ON user_provider_oauth_tokens(state)
  WHERE state != '';
