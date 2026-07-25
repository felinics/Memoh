-- 0039_drop_inbox
-- Remove bot_inbox table and max_inbox_items column from bots.

DROP TABLE IF EXISTS bot_inbox;

ALTER TABLE bots DROP COLUMN IF EXISTS max_inbox_items;
