-- 0123_compaction_default_policy
-- Restore the legacy opt-in compaction defaults.

ALTER TABLE bots ALTER COLUMN compaction_threshold SET DEFAULT 100000;
ALTER TABLE bots ALTER COLUMN compaction_enabled SET DEFAULT false;
