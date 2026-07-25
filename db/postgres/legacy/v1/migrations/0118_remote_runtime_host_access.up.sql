-- 0118_remote_runtime_host_access
-- Remove the obsolete per-Bot directory boundary from Remote Runtime bindings.

ALTER TABLE IF EXISTS bot_remote_runtime_bindings
  DROP COLUMN IF EXISTS workspace_path;
