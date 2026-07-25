-- 0058_fix_session_events_dedup (rollback)
-- Restore the original dedup index without event_kind.

DROP INDEX IF EXISTS idx_session_events_dedup;
CREATE UNIQUE INDEX IF NOT EXISTS idx_session_events_dedup
  ON bot_session_events (session_id, external_message_id)
  WHERE external_message_id IS NOT NULL AND external_message_id != '';
