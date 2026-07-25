-- 0024_drop_host_path (rollback)
-- Re-add host_path column to containers table
ALTER TABLE containers ADD COLUMN IF NOT EXISTS host_path TEXT;
