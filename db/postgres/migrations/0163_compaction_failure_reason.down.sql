-- 0163_compaction_failure_reason
-- Drop the compaction attempt failure reason.

ALTER TABLE bot_history_message_compacts
  DROP COLUMN IF EXISTS failure_reason;
