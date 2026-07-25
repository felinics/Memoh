-- 0059_message_event_id
-- Add event_id column to bot_history_messages so that user messages can
-- reference their canonical event for clean frontend display.

ALTER TABLE bot_history_messages
  ADD COLUMN IF NOT EXISTS event_id UUID REFERENCES bot_session_events(id) ON DELETE SET NULL;
