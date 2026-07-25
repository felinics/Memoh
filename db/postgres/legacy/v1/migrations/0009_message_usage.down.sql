-- 0009_message_usage
-- Remove usage JSONB column from bot_history_messages

ALTER TABLE bot_history_messages DROP COLUMN IF EXISTS usage;
