-- 0029_tts_provider
-- Revert TTS provider/model tables and bots.tts_model_id.

ALTER TABLE bots
  DROP CONSTRAINT IF EXISTS bots_tts_model_id_fkey;

ALTER TABLE bots
  DROP COLUMN IF EXISTS tts_model_id;

DROP INDEX IF EXISTS idx_tts_models_provider_id;

DROP TABLE IF EXISTS tts_models;

DROP TABLE IF EXISTS tts_providers;
