-- 0050_tts_provider_enable (down)
-- Remove the enable column from tts_providers.

ALTER TABLE tts_providers DROP COLUMN IF EXISTS enable;
