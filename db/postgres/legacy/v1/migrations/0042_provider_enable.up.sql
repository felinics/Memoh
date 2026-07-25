-- 0042_provider_enable
-- Add enable column to llm_providers for built-in provider registry support.

ALTER TABLE IF EXISTS llm_providers
  ADD COLUMN IF NOT EXISTS enable BOOLEAN NOT NULL DEFAULT true;
