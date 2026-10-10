-- 0164_compaction_progress
-- Let compaction move past history it cannot shrink.
-- failure_reason records why an attempt failed when selection acts on it:
-- 'ineffective_summary' marks rows whose summary was not shorter than them;
-- within the epoch they are only resent together with new rows;
-- 'unusable_summary' marks rows the model returned no usable summary for,
-- held back for a while that grows with failure_attempts, the number of
-- consecutive such attempts on them.
-- compaction_scan_after is the last candidate row a pass found permanently
-- unclaimable in compaction_scan_epoch; later passes in that epoch start
-- reading after it instead of rescanning the same prefix.

ALTER TABLE bot_history_message_compacts
  ADD COLUMN IF NOT EXISTS failure_reason TEXT NOT NULL DEFAULT '';

ALTER TABLE bot_history_message_compacts
  ADD COLUMN IF NOT EXISTS failure_attempts INTEGER NOT NULL DEFAULT 0;

ALTER TABLE bot_sessions
  ADD COLUMN IF NOT EXISTS compaction_scan_after UUID;

ALTER TABLE bot_sessions
  ADD COLUMN IF NOT EXISTS compaction_scan_epoch BIGINT NOT NULL DEFAULT 0;
