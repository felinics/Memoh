-- 0061_unify_providers (rollback)
-- Reverse the provider unification: restore llm_providers, tts_providers, tts_models.

-- Step 1: Rename provider_oauth_tokens back
ALTER INDEX IF EXISTS idx_provider_oauth_tokens_state RENAME TO idx_llm_provider_oauth_tokens_state;
ALTER TABLE provider_oauth_tokens RENAME COLUMN provider_id TO llm_provider_id;
ALTER TABLE provider_oauth_tokens RENAME TO llm_provider_oauth_tokens;

-- Step 2: Recreate tts_providers and tts_models
CREATE TABLE IF NOT EXISTS tts_providers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  provider TEXT NOT NULL,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  enable BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT tts_providers_name_unique UNIQUE (name)
);

CREATE TABLE IF NOT EXISTS tts_models (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  model_id TEXT NOT NULL,
  name TEXT,
  tts_provider_id UUID NOT NULL REFERENCES tts_providers(id) ON DELETE CASCADE,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_tts_models_provider_id ON tts_models(tts_provider_id);

-- Step 3: Migrate speech providers back to tts_providers
INSERT INTO tts_providers (id, name, provider, config, enable, created_at, updated_at)
SELECT id, name,
  CASE WHEN client_type = 'edge-speech' THEN 'edge' ELSE client_type END,
  config, enable, created_at, updated_at
FROM providers
WHERE client_type = 'edge-speech';

-- Step 4: Migrate speech models back to tts_models
INSERT INTO tts_models (id, model_id, name, tts_provider_id, config, created_at, updated_at)
SELECT id, model_id, name, provider_id, config, created_at, updated_at
FROM models
WHERE type = 'speech';

-- Step 5: Update bots FK back to tts_models
ALTER TABLE bots DROP CONSTRAINT IF EXISTS bots_tts_model_id_fkey;
ALTER TABLE bots ADD CONSTRAINT bots_tts_model_id_fkey
  FOREIGN KEY (tts_model_id) REFERENCES tts_models(id) ON DELETE SET NULL;

-- Step 6: Remove speech models and providers from unified tables
DELETE FROM models WHERE type = 'speech';
DELETE FROM providers WHERE client_type = 'edge-speech';

-- Step 7: Restore models type CHECK
ALTER TABLE models DROP CONSTRAINT models_type_check;
ALTER TABLE models ADD CONSTRAINT models_type_check CHECK (type IN ('chat', 'embedding'));

-- Step 8: Rename provider_id back to llm_provider_id
ALTER TABLE models RENAME CONSTRAINT models_provider_id_model_id_unique TO models_provider_model_id_unique;
ALTER TABLE models RENAME COLUMN provider_id TO llm_provider_id;

-- Step 9: Restore client_type CHECK
ALTER TABLE providers DROP CONSTRAINT providers_client_type_check;
ALTER TABLE providers ADD CONSTRAINT llm_providers_client_type_check CHECK (
  client_type IN ('openai-responses', 'openai-completions', 'anthropic-messages', 'google-generative-ai', 'openai-codex')
);

-- Step 10: Restore api_key and base_url columns from config
ALTER TABLE providers ADD COLUMN base_url TEXT NOT NULL DEFAULT '';
ALTER TABLE providers ADD COLUMN api_key TEXT NOT NULL DEFAULT '';
UPDATE providers SET
  base_url = COALESCE(config->>'base_url', ''),
  api_key = COALESCE(config->>'api_key', '');
ALTER TABLE providers DROP COLUMN config;

-- Step 11: Rename providers back to llm_providers
ALTER TABLE providers RENAME CONSTRAINT providers_name_unique TO llm_providers_name_unique;
ALTER TABLE providers RENAME TO llm_providers;
