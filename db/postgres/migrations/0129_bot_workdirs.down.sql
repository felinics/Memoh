-- 0129_bot_workdirs
-- Drop the per-bot working directory table and the session binding column.

DROP INDEX IF EXISTS idx_bot_sessions_workdir_active_updated;

ALTER TABLE public.bot_sessions
    DROP CONSTRAINT IF EXISTS bot_sessions_workdir_id_fkey;
ALTER TABLE public.bot_sessions
    DROP COLUMN IF EXISTS workdir_id;

DROP TABLE IF EXISTS public.bot_workdirs;
