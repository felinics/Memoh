-- 0125_compaction_target_percent
-- Restore the legacy compaction ratio from the window keep-share override.

ALTER TABLE bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bots DISABLE ROW LEVEL SECURITY;

ALTER TABLE bots
  ADD COLUMN IF NOT EXISTS compaction_ratio INTEGER NOT NULL DEFAULT 80;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM pg_attribute
    WHERE attrelid = 'bots'::regclass
      AND attname = 'compaction_target_percent'
      AND NOT attisdropped
  ) THEN
    UPDATE bots
    SET compaction_ratio = GREATEST(1, LEAST(100, 100 - compaction_target_percent))
    WHERE compaction_target_percent IS NOT NULL;
  END IF;
END
$$;

ALTER TABLE bots
  DROP COLUMN IF EXISTS compaction_target_percent;

ALTER TABLE bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE bots FORCE ROW LEVEL SECURITY;
