-- 0011_add_inbox (down)
-- Remove bot_inbox table and max_inbox_items column.

DROP INDEX IF EXISTS idx_bot_inbox_bot_created;
DROP INDEX IF EXISTS idx_bot_inbox_bot_unread;
DROP TABLE IF EXISTS bot_inbox;

ALTER TABLE bots DROP COLUMN IF EXISTS max_inbox_items;
