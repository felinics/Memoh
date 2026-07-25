-- 0063_add_task_tracking
-- Add exec_id and pid columns to tasks table for process tracking.

CREATE TABLE IF NOT EXISTS tasks (
    id VARCHAR(255) PRIMARY KEY,
    bot_id VARCHAR(255) NOT NULL,
    name VARCHAR(255) NOT NULL,
    command TEXT NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS exec_id VARCHAR(255) NULL;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS pid INTEGER NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_exec_id ON tasks(exec_id);
CREATE INDEX IF NOT EXISTS idx_tasks_pid ON tasks(pid);
