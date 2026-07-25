-- 0075_overlay_providers
-- Add bot-scoped SD-WAN network configuration fields.
-- Each bot gets its own isolated sidecar instance so a single overlay_config
-- JSONB column is sufficient — no provider/binding split needed.

ALTER TABLE bots
  ADD COLUMN IF NOT EXISTS overlay_provider TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS overlay_enabled  BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS overlay_config   JSONB NOT NULL DEFAULT '{}'::jsonb;
