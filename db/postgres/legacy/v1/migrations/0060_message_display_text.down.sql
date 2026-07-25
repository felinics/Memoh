-- 0060_message_display_text (rollback)
ALTER TABLE bot_history_messages DROP COLUMN IF EXISTS display_text;
