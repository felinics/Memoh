-- 0059_message_event_id (rollback)
ALTER TABLE bot_history_messages DROP COLUMN IF EXISTS event_id;
