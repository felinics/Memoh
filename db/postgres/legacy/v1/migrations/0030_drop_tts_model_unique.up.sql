-- 0030_drop_tts_model_unique
-- Drop unique constraint on (tts_provider_id, model_id) to allow multiple
-- models with the same model_id under one provider (different configs).

ALTER TABLE tts_models
  DROP CONSTRAINT IF EXISTS tts_models_provider_model_id_unique;
