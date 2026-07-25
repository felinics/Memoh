-- 0062_github_copilot_user_oauth (rollback)
-- Remove user-scoped provider OAuth tokens and github-copilot client type.

DROP INDEX IF EXISTS idx_user_provider_oauth_tokens_state;
DROP TABLE IF EXISTS user_provider_oauth_tokens;

DELETE FROM providers WHERE client_type = 'github-copilot';

ALTER TABLE IF EXISTS providers DROP CONSTRAINT IF EXISTS providers_client_type_check;

ALTER TABLE IF EXISTS providers
  ADD CONSTRAINT providers_client_type_check CHECK (
    client_type IN (
      'openai-responses',
      'openai-completions',
      'anthropic-messages',
      'google-generative-ai',
      'openai-codex',
      'edge-speech'
    )
  );
