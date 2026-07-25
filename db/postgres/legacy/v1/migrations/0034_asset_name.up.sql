-- 0034_asset_name
-- Add name column to bot_history_message_assets to preserve original filenames.

ALTER TABLE bot_history_message_assets
  ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';
