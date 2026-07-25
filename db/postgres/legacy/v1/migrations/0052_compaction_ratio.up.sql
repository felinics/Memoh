-- 0052_compaction_ratio
-- Add compaction_ratio column to bots table for controlling what percentage of messages to compact.

ALTER TABLE bots ADD COLUMN IF NOT EXISTS compaction_ratio INTEGER NOT NULL DEFAULT 80;
