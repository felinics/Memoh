-- 0050_tts_provider_enable
-- Add enable column to tts_providers table for toggling providers on/off.

ALTER TABLE tts_providers ADD COLUMN IF NOT EXISTS enable BOOLEAN NOT NULL DEFAULT false;
