-- 0040_compaction (down)
-- Revert context compaction support.

ALTER TABLE bots DROP COLUMN IF EXISTS compaction_model_id;
ALTER TABLE bots DROP COLUMN IF EXISTS compaction_threshold;
ALTER TABLE bots DROP COLUMN IF EXISTS compaction_enabled;

DROP INDEX IF EXISTS idx_bot_history_messages_compact;
ALTER TABLE bot_history_messages DROP COLUMN IF EXISTS compact_id;

DROP INDEX IF EXISTS idx_compacts_bot_session;
DROP TABLE IF EXISTS bot_history_message_compacts;
