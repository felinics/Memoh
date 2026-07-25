-- 0029_tts_provider
-- Add TTS provider/model tables and bots.tts_model_id for existing databases.

CREATE TABLE IF NOT EXISTS tts_providers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  provider TEXT NOT NULL,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT tts_providers_name_unique UNIQUE (name)
);

CREATE TABLE IF NOT EXISTS tts_models (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  model_id TEXT NOT NULL,
  name TEXT,
  tts_provider_id UUID NOT NULL REFERENCES tts_providers(id) ON DELETE CASCADE,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT tts_models_provider_model_id_unique UNIQUE (tts_provider_id, model_id)
);

CREATE INDEX IF NOT EXISTS idx_tts_models_provider_id ON tts_models(tts_provider_id);

ALTER TABLE bots
  ADD COLUMN IF NOT EXISTS tts_model_id UUID;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'bots_tts_model_id_fkey'
  ) THEN
    ALTER TABLE bots
      ADD CONSTRAINT bots_tts_model_id_fkey
      FOREIGN KEY (tts_model_id) REFERENCES tts_models(id) ON DELETE SET NULL;
  END IF;
END $$;
