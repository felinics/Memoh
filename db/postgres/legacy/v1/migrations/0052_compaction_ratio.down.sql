-- 0052_compaction_ratio (rollback)
-- Remove compaction_ratio column from bots table.

ALTER TABLE bots DROP COLUMN IF EXISTS compaction_ratio;
