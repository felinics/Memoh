-- 0058_fix_session_events_dedup
-- Rebuild dedup index to include event_kind so that message + edit for the
-- same external_message_id are stored as separate events.

DROP INDEX IF EXISTS idx_session_events_dedup;
CREATE UNIQUE INDEX IF NOT EXISTS idx_session_events_dedup
  ON bot_session_events (session_id, event_kind, external_message_id)
  WHERE external_message_id IS NOT NULL AND external_message_id != '';
