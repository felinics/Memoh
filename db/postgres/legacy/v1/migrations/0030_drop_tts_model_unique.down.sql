-- 0030_drop_tts_model_unique (rollback)
-- Re-add the unique constraint. Duplicates must be resolved manually first.

ALTER TABLE tts_models
  ADD CONSTRAINT tts_models_provider_model_id_unique UNIQUE (tts_provider_id, model_id);
