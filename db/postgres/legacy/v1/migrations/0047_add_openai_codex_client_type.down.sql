-- 0047_add_openai_codex_client_type (rollback)
-- Revert openai-codex rows back to openai-responses and restore the old CHECK constraint.

UPDATE llm_providers
SET client_type = 'openai-responses',
    updated_at  = now()
WHERE client_type = 'openai-codex';

ALTER TABLE llm_providers DROP CONSTRAINT IF EXISTS llm_providers_client_type_check;
ALTER TABLE llm_providers ADD CONSTRAINT llm_providers_client_type_check
  CHECK (client_type IN ('openai-responses', 'openai-completions', 'anthropic-messages', 'google-generative-ai'));
