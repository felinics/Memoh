-- 0076_container_workspace_backend
-- Add explicit workspace backend tracking for workspace runtimes.

ALTER TABLE containers
  ADD COLUMN IF NOT EXISTS workspace_backend TEXT NOT NULL DEFAULT 'container';
