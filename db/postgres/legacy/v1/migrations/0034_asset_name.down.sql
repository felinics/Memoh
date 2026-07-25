-- 0034_asset_name (rollback)
-- Remove name column from bot_history_message_assets.

ALTER TABLE bot_history_message_assets
  DROP COLUMN IF EXISTS name;
