-- 0041_provider_model_refactor (rollback)
-- Restore client_type/dimensions/input_modalities/supports_reasoning to models, remove from providers.

-- 1. Re-add old columns to models
ALTER TABLE models
  ADD COLUMN IF NOT EXISTS client_type TEXT,
  ADD COLUMN IF NOT EXISTS dimensions INTEGER,
  ADD COLUMN IF NOT EXISTS input_modalities TEXT[] NOT NULL DEFAULT ARRAY['text']::TEXT[],
  ADD COLUMN IF NOT EXISTS supports_reasoning BOOLEAN NOT NULL DEFAULT false;

-- 2. Migrate config back to columns
UPDATE models SET
  dimensions = (config->>'dimensions')::INTEGER,
  supports_reasoning = COALESCE(config->'compatibilities' @> '"reasoning"', false),
  input_modalities = CASE
    WHEN config->'compatibilities' @> '"vision"' THEN ARRAY['text','image']::TEXT[]
    ELSE ARRAY['text']::TEXT[]
  END;

-- 3. Back-fill model client_type from provider
UPDATE models m
SET client_type = p.client_type
FROM llm_providers p
WHERE m.llm_provider_id = p.id;

-- 4. Re-add constraints
ALTER TABLE models
  ADD CONSTRAINT models_client_type_check CHECK (client_type IS NULL OR client_type IN ('openai-responses', 'openai-completions', 'anthropic-messages', 'google-generative-ai')),
  ADD CONSTRAINT models_chat_client_type_check CHECK (type != 'chat' OR client_type IS NOT NULL),
  ADD CONSTRAINT models_dimensions_check CHECK (type != 'embedding' OR dimensions IS NOT NULL);

-- 5. Drop config from models
ALTER TABLE models DROP COLUMN IF EXISTS config;

-- 6. Drop provider columns and constraint
ALTER TABLE llm_providers DROP CONSTRAINT IF EXISTS llm_providers_client_type_check;
ALTER TABLE llm_providers DROP COLUMN IF EXISTS client_type;
ALTER TABLE llm_providers DROP COLUMN IF EXISTS icon;
