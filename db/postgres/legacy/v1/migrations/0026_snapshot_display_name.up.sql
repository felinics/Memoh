-- 0026_snapshot_display_name
-- Add user-facing display_name to snapshots while keeping runtime_snapshot_name
-- as the internal containerd identifier.

ALTER TABLE snapshots
  ADD COLUMN IF NOT EXISTS display_name TEXT;
