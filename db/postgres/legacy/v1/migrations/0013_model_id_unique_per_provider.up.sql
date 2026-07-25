-- 0013_model_id_unique_per_provider
-- Change model_id uniqueness from global to per provider.

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'models_model_id_unique') THEN
    ALTER TABLE models DROP CONSTRAINT models_model_id_unique;
  END IF;

  -- Only add old-style constraint when llm_provider_id column exists (pre-0061 schema).
  -- Fresh databases already have provider_id with models_provider_id_model_id_unique.
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'models_provider_model_id_unique')
     AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'models' AND column_name = 'llm_provider_id')
  THEN
    ALTER TABLE models
      ADD CONSTRAINT models_provider_model_id_unique UNIQUE (llm_provider_id, model_id);
  END IF;
END
$$;
