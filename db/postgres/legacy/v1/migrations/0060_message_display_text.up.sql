-- 0060_message_display_text
-- Add display_text column to store raw user message text for frontend display.

ALTER TABLE bot_history_messages
  ADD COLUMN IF NOT EXISTS display_text TEXT;
