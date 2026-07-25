-- 0009_message_usage
-- Add usage JSONB column to bot_history_messages for storing LLM token usage

ALTER TABLE bot_history_messages ADD COLUMN IF NOT EXISTS usage JSONB;
