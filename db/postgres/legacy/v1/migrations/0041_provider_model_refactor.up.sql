-- 0041_provider_model_refactor
-- Move client_type to llm_providers, add icon, replace model columns with config JSONB.

-- 1. Add client_type and icon to llm_providers (IF EXISTS for fresh-schema compat)
ALTER TABLE IF EXISTS llm_providers
  ADD COLUMN IF NOT EXISTS client_type TEXT NOT NULL DEFAULT 'openai-completions',
  ADD COLUMN IF NOT EXISTS icon TEXT;

-- 2–6. Backfill and migrate only when old columns exist (idempotent for fresh DBs).
DO $$ BEGIN
  -- Back-fill provider client_type from models.client_type (old column).
  -- Only runs on pre-0061 schema where llm_providers table still exists.
  IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'llm_providers')
     AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'models' AND column_name = 'client_type')
  THEN
    UPDATE llm_providers p
    SET client_type = sub.client_type
    FROM (
      SELECT DISTINCT ON (llm_provider_id) llm_provider_id, client_type
      FROM models
      WHERE client_type IS NOT NULL AND client_type != ''
      ORDER BY llm_provider_id, created_at ASC
    ) sub
    WHERE p.id = sub.llm_provider_id;
  END IF;

  -- Add CHECK constraint (skip if already present or table renamed)
  IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'llm_providers')
     AND NOT EXISTS (SELECT 1 FROM information_schema.table_constraints WHERE constraint_name = 'llm_providers_client_type_check')
  THEN
    ALTER TABLE llm_providers
      ADD CONSTRAINT llm_providers_client_type_check
      CHECK (client_type IN ('openai-responses', 'openai-completions', 'anthropic-messages', 'google-generative-ai'));
  END IF;

  -- Add config JSONB to models
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'models' AND column_name = 'config') THEN
    ALTER TABLE models ADD COLUMN config JSONB NOT NULL DEFAULT '{}'::jsonb;
  END IF;

  -- Migrate existing columns into config (only when old columns exist)
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'models' AND column_name = 'dimensions') THEN
    UPDATE models SET config = jsonb_strip_nulls(jsonb_build_object(
      'dimensions', dimensions,
      'compatibilities', (
        SELECT coalesce(jsonb_agg(c), '[]'::jsonb) FROM (
          SELECT 'tool-call' AS c
          UNION ALL
          SELECT 'vision' WHERE 'image' = ANY(input_modalities)
          UNION ALL
          SELECT 'reasoning' WHERE supports_reasoning = true
        ) AS caps
      ),
      'context_window', NULL
    ));
  END IF;
END $$;

-- Drop old columns and constraints (IF EXISTS makes these safe on fresh DBs)
ALTER TABLE models DROP CONSTRAINT IF EXISTS models_client_type_check;
ALTER TABLE models DROP CONSTRAINT IF EXISTS models_chat_client_type_check;
ALTER TABLE models DROP CONSTRAINT IF EXISTS models_dimensions_check;

ALTER TABLE models DROP COLUMN IF EXISTS client_type;
ALTER TABLE models DROP COLUMN IF EXISTS dimensions;
ALTER TABLE models DROP COLUMN IF EXISTS input_modalities;
ALTER TABLE models DROP COLUMN IF EXISTS supports_reasoning;
