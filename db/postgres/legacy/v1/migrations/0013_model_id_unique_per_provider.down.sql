-- 0013_model_id_unique_per_provider
-- Revert model_id uniqueness back to global uniqueness.

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM models
    GROUP BY model_id
    HAVING COUNT(*) > 1
  ) THEN
    RAISE EXCEPTION 'cannot rollback 0013_model_id_unique_per_provider: duplicate model_id values exist across providers';
  END IF;

  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'models_provider_model_id_unique') THEN
    ALTER TABLE models DROP CONSTRAINT models_provider_model_id_unique;
  END IF;

  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'models_model_id_unique') THEN
    ALTER TABLE models
      ADD CONSTRAINT models_model_id_unique UNIQUE (model_id);
  END IF;
END
$$;
