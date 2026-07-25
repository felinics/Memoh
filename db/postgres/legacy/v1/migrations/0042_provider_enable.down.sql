-- 0042_provider_enable (rollback)
-- Remove enable column from llm_providers.

ALTER TABLE llm_providers
  DROP COLUMN IF EXISTS enable;
