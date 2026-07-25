-- 0109_compaction_epoch (rollback)
-- Restore nullable artifact session ownership and remove epoch fencing.

DROP INDEX IF EXISTS idx_compacts_owner_epoch;

ALTER TABLE bot_history_message_compacts
  DROP CONSTRAINT IF EXISTS bot_history_message_compacts_session_id_fkey;
ALTER TABLE bot_history_message_compacts
  ADD CONSTRAINT bot_history_message_compacts_session_id_fkey
  FOREIGN KEY (session_id) REFERENCES bot_sessions(id) ON DELETE SET NULL;

ALTER TABLE bot_history_message_compacts
  DROP COLUMN IF EXISTS compaction_epoch;

ALTER TABLE bot_sessions
  DROP COLUMN IF EXISTS compaction_epoch;
