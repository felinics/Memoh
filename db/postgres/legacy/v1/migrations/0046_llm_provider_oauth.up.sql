-- 0046_llm_provider_oauth
-- Add OAuth token storage for LLM providers to support OpenAI Codex OAuth.
-- On fresh databases, provider_oauth_tokens is already created by 0001_init.

DO $$
BEGIN
  -- Only create old-style table when llm_providers still exists (pre-0061 schema).
  IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'llm_providers')
     AND NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'llm_provider_oauth_tokens')
  THEN
    CREATE TABLE llm_provider_oauth_tokens (
      id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
      llm_provider_id UUID NOT NULL UNIQUE REFERENCES llm_providers(id) ON DELETE CASCADE,
      access_token TEXT NOT NULL DEFAULT '',
      refresh_token TEXT NOT NULL DEFAULT '',
      expires_at TIMESTAMPTZ,
      scope TEXT NOT NULL DEFAULT '',
      token_type TEXT NOT NULL DEFAULT '',
      state TEXT NOT NULL DEFAULT '',
      pkce_code_verifier TEXT NOT NULL DEFAULT '',
      created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
      updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
    );
    CREATE INDEX idx_llm_provider_oauth_tokens_state ON llm_provider_oauth_tokens(state) WHERE state != '';
  END IF;
END $$;
