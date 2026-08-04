-- 0129_bot_projects
-- Drop the per-bot project directory table and the session binding column.

DROP INDEX IF EXISTS idx_bot_sessions_project_active_updated;

ALTER TABLE public.bot_sessions
    DROP CONSTRAINT IF EXISTS bot_sessions_project_id_fkey;
ALTER TABLE public.bot_sessions
    DROP COLUMN IF EXISTS project_id;

DROP TABLE IF EXISTS public.bot_projects;
