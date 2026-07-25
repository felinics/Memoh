-- 0035_asset_metadata (rollback)
-- Remove metadata column from bot_history_message_assets.

ALTER TABLE bot_history_message_assets
  DROP COLUMN IF EXISTS metadata;
