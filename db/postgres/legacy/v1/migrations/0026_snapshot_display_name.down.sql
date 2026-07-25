-- 0026_snapshot_display_name (rollback)
-- Remove the user-facing display_name from snapshots.

ALTER TABLE snapshots
  DROP COLUMN IF EXISTS display_name;
