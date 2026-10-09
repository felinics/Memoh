-- 0163_compaction_progress
-- Drop the compaction scan position and the attempt failure reason.

ALTER TABLE bot_sessions
  DROP COLUMN IF EXISTS compaction_scan_epoch;

ALTER TABLE bot_sessions
  DROP COLUMN IF EXISTS compaction_scan_after;

ALTER TABLE bot_history_message_compacts
  DROP COLUMN IF EXISTS failure_reason;
