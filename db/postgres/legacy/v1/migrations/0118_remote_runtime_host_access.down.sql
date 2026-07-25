-- 0118_remote_runtime_host_access
-- Restore the legacy per-Bot Remote Runtime workspace path.

ALTER TABLE IF EXISTS bot_remote_runtime_bindings
  ADD COLUMN IF NOT EXISTS workspace_path TEXT;

UPDATE bot_remote_runtime_bindings
SET workspace_path = '.'
WHERE workspace_path IS NULL OR btrim(workspace_path) = '';

ALTER TABLE IF EXISTS bot_remote_runtime_bindings
  ALTER COLUMN workspace_path SET NOT NULL;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'bot_remote_runtime_bindings_workspace_path_check'
      AND conrelid = 'bot_remote_runtime_bindings'::regclass
  ) THEN
    ALTER TABLE bot_remote_runtime_bindings
      ADD CONSTRAINT bot_remote_runtime_bindings_workspace_path_check
      CHECK (btrim(workspace_path) <> '');
  END IF;
END
$$;
