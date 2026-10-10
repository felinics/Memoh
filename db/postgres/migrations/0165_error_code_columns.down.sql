-- 0165_error_code_columns (down)
ALTER TABLE public.bot_workspaces
  DROP COLUMN IF EXISTS last_error_code;

ALTER TABLE public.schedule_logs
  DROP COLUMN IF EXISTS error_code;
