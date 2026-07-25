-- 0035_asset_metadata
-- Add metadata JSONB column to bot_history_message_assets for source_path, source_url, etc.

ALTER TABLE bot_history_message_assets
  ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb;
