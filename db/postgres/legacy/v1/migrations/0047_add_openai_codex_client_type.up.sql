-- 0047_add_openai_codex_client_type
-- Add openai-codex as a first-class client_type and migrate existing codex-oauth providers.
-- On fresh databases, providers table already has the expanded CHECK from 0001_init.

ALTER TABLE IF EXISTS llm_providers DROP CONSTRAINT IF EXISTS llm_providers_client_type_check;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'llm_providers') THEN
    ALTER TABLE llm_providers ADD CONSTRAINT llm_providers_client_type_check
      CHECK (client_type IN ('openai-responses', 'openai-completions', 'anthropic-messages', 'google-generative-ai', 'openai-codex'));

    UPDATE llm_providers
    SET client_type = 'openai-codex',
        updated_at  = now()
    WHERE client_type = 'openai-responses'
      AND metadata->>'auth_type' = 'openai-codex-oauth';
  END IF;
END $$;
