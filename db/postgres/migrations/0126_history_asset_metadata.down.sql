-- 0126_history_asset_metadata
-- Remove denormalized attachment metadata from message history.

ALTER TABLE bot_history_message_assets
  DROP COLUMN IF EXISTS storage_key,
  DROP COLUMN IF EXISTS size_bytes,
  DROP COLUMN IF EXISTS mime;
