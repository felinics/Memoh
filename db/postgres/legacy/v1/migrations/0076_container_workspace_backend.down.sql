-- 0076_container_workspace_backend
-- Remove explicit workspace backend tracking from containers.

ALTER TABLE containers
  DROP COLUMN IF EXISTS workspace_backend;
