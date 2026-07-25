-- 0070_add_bot_transcription_model
-- Add bots.transcription_model_id for bot-level speech-to-text defaults.

ALTER TABLE bots
  ADD COLUMN IF NOT EXISTS transcription_model_id UUID REFERENCES models(id) ON DELETE SET NULL;
