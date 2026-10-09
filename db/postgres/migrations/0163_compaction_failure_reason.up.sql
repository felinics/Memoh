-- 0163_compaction_failure_reason
-- Record why a compaction attempt failed when candidate selection acts on it.
-- 'ineffective_summary' marks an attempt whose summary was not shorter than
-- the rows it would replace. Within the same compaction epoch those rows stay
-- raw in place and later passes move past them instead of resending them.

ALTER TABLE bot_history_message_compacts
  ADD COLUMN IF NOT EXISTS failure_reason TEXT NOT NULL DEFAULT '';
