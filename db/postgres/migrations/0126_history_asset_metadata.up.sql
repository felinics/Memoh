-- 0126_history_asset_metadata
-- Persist attachment metadata so history reads never probe runtime storage.

ALTER TABLE bot_history_message_assets
  ADD COLUMN IF NOT EXISTS mime TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS size_bytes BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS storage_key TEXT NOT NULL DEFAULT '';
