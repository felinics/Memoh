-- 0063_add_task_tracking (rollback)
-- Remove exec_id and pid columns from tasks table.

DROP INDEX IF EXISTS idx_tasks_pid;
DROP INDEX IF EXISTS idx_tasks_exec_id;
ALTER TABLE tasks DROP COLUMN IF EXISTS pid;
ALTER TABLE tasks DROP COLUMN IF EXISTS exec_id;
