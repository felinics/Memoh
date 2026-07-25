-- 0070_add_bot_transcription_model
-- Remove bots.transcription_model_id.

ALTER TABLE bots
  DROP CONSTRAINT IF EXISTS bots_transcription_model_id_fkey;

ALTER TABLE bots
  DROP COLUMN IF EXISTS transcription_model_id;
