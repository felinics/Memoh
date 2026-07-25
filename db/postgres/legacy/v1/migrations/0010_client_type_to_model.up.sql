-- 0010_client_type_to_model
-- Move client_type from llm_providers to models, rename to new values, drop unsupported types.

-- 1) Add client_type column to models (nullable)
ALTER TABLE models ADD COLUMN IF NOT EXISTS client_type TEXT;

-- 2-5) Only run data migration when upgrading from old schema that had client_type on llm_providers.
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'llm_providers' AND column_name = 'client_type'
  ) THEN
    -- Migrate data from provider to model with name mapping
    UPDATE models SET client_type = CASE p.client_type
        WHEN 'openai' THEN 'openai-responses'
        WHEN 'openai-compat' THEN 'openai-completions'
        WHEN 'anthropic' THEN 'anthropic-messages'
        WHEN 'google' THEN 'google-generative-ai'
    END
    FROM llm_providers p
    WHERE models.llm_provider_id = p.id
      AND p.client_type IN ('openai', 'openai-compat', 'anthropic', 'google');

    -- Delete models whose provider had an unsupported client_type
    DELETE FROM models WHERE client_type IS NULL AND type = 'chat';

    -- Delete providers with unsupported client_type
    DELETE FROM llm_providers WHERE client_type NOT IN ('openai', 'openai-compat', 'anthropic', 'google');
  END IF;
END $$;

-- 6) Add CHECK constraints (skip if already present from init migration)
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'models_client_type_check') THEN
    ALTER TABLE models ADD CONSTRAINT models_client_type_check
      CHECK (client_type IS NULL OR client_type IN ('openai-responses', 'openai-completions', 'anthropic-messages', 'google-generative-ai'));
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'models_chat_client_type_check') THEN
    ALTER TABLE models ADD CONSTRAINT models_chat_client_type_check
      CHECK (type != 'chat' OR client_type IS NOT NULL);
  END IF;
END $$;

-- 7) Drop client_type from llm_providers (IF EXISTS for fresh-schema compat)
ALTER TABLE IF EXISTS llm_providers DROP CONSTRAINT IF EXISTS llm_providers_client_type_check;
ALTER TABLE IF EXISTS llm_providers DROP COLUMN IF EXISTS client_type;
