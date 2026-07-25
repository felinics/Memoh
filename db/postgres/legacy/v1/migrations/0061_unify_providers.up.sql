-- 0061_unify_providers
-- Unify llm_providers and tts_providers/tts_models into a single providers/models schema.
-- Merge api_key and base_url into a config JSONB column. Add speech model type.
-- NOTE: On fresh databases the canonical schema already applies; all guards are IF EXISTS.

DO $$
BEGIN
  -- Only run full migration if old llm_providers table still exists
  IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'llm_providers') THEN
    RETURN;
  END IF;

  -- Step 1: Rename llm_providers → providers
  EXECUTE 'ALTER TABLE llm_providers RENAME TO providers';
  EXECUTE 'ALTER TABLE providers RENAME CONSTRAINT llm_providers_name_unique TO providers_name_unique';
  EXECUTE 'ALTER TABLE providers DROP CONSTRAINT IF EXISTS llm_providers_client_type_check';

  -- Step 2: Add config JSONB and migrate api_key + base_url into it
  EXECUTE 'ALTER TABLE providers ADD COLUMN IF NOT EXISTS config JSONB NOT NULL DEFAULT ''{}''::jsonb';
  EXECUTE 'UPDATE providers SET config = jsonb_build_object(''api_key'', api_key, ''base_url'', base_url) WHERE api_key IS NOT NULL';
  EXECUTE 'ALTER TABLE providers DROP COLUMN IF EXISTS api_key';
  EXECUTE 'ALTER TABLE providers DROP COLUMN IF EXISTS base_url';

  -- Step 3: Expand client_type CHECK
  EXECUTE 'ALTER TABLE providers ADD CONSTRAINT providers_client_type_check CHECK (
    client_type IN (
      ''openai-responses'', ''openai-completions'', ''anthropic-messages'',
      ''google-generative-ai'', ''openai-codex'', ''edge-speech''
    )
  )';

  -- Step 4: Rename llm_provider_id → provider_id in models table
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'models' AND column_name = 'llm_provider_id') THEN
    EXECUTE 'ALTER TABLE models RENAME COLUMN llm_provider_id TO provider_id';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'models_provider_model_id_unique') THEN
    EXECUTE 'ALTER TABLE models RENAME CONSTRAINT models_provider_model_id_unique TO models_provider_id_model_id_unique';
  END IF;

  -- Step 5: Expand models type CHECK to include speech
  EXECUTE 'ALTER TABLE models DROP CONSTRAINT IF EXISTS models_type_check';
  EXECUTE 'ALTER TABLE models ADD CONSTRAINT models_type_check CHECK (type IN (''chat'', ''embedding'', ''speech''))';

  -- Step 6: Migrate tts_providers into providers
  IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'tts_providers') THEN
    EXECUTE '
      INSERT INTO providers (id, name, client_type, icon, enable, config, metadata, created_at, updated_at)
      SELECT
        tp.id,
        tp.name,
        CASE WHEN tp.provider = ''edge'' THEN ''edge-speech'' ELSE tp.provider END,
        NULL,
        tp.enable,
        tp.config,
        ''{}''::jsonb,
        tp.created_at,
        tp.updated_at
      FROM tts_providers tp
      WHERE NOT EXISTS (SELECT 1 FROM providers p WHERE p.id = tp.id)';
  END IF;

  -- Step 7: Migrate tts_models into models
  IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'tts_models') THEN
    EXECUTE '
      INSERT INTO models (id, model_id, name, provider_id, type, config, created_at, updated_at)
      SELECT
        tm.id,
        tm.model_id,
        tm.name,
        tm.tts_provider_id,
        ''speech'',
        tm.config,
        tm.created_at,
        tm.updated_at
      FROM tts_models tm
      WHERE NOT EXISTS (SELECT 1 FROM models m WHERE m.id = tm.id)';
  END IF;

  -- Step 8: Update bots.tts_model_id FK to reference models instead of tts_models
  EXECUTE 'ALTER TABLE bots DROP CONSTRAINT IF EXISTS bots_tts_model_id_fkey';
  EXECUTE 'ALTER TABLE bots ADD CONSTRAINT bots_tts_model_id_fkey FOREIGN KEY (tts_model_id) REFERENCES models(id) ON DELETE SET NULL';

  -- Step 9: Drop tts_models and tts_providers
  EXECUTE 'DROP INDEX IF EXISTS idx_tts_models_provider_id';
  EXECUTE 'DROP TABLE IF EXISTS tts_models';
  EXECUTE 'DROP TABLE IF EXISTS tts_providers';

  -- Step 10: Rename llm_provider_oauth_tokens → provider_oauth_tokens
  IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'llm_provider_oauth_tokens') THEN
    EXECUTE 'ALTER TABLE llm_provider_oauth_tokens RENAME TO provider_oauth_tokens';
    EXECUTE 'ALTER TABLE provider_oauth_tokens RENAME COLUMN llm_provider_id TO provider_id';
    EXECUTE 'ALTER INDEX IF EXISTS idx_llm_provider_oauth_tokens_state RENAME TO idx_provider_oauth_tokens_state';
  END IF;
END $$;
