-- 0075_overlay_providers
-- Remove bot-scoped network configuration fields.

ALTER TABLE bots
  DROP COLUMN IF EXISTS overlay_config,
  DROP COLUMN IF EXISTS overlay_enabled,
  DROP COLUMN IF EXISTS overlay_provider;
