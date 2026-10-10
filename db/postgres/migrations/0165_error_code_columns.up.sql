-- 0165_error_code_columns
-- schedule_logs.error_message and bot_workspaces.last_error hold English
-- sentences and error text written by older servers. New failures store the
-- public catalog code in error_code / last_error_code and leave the text
-- column empty; clients render errors.<code> when the code is set and show the
-- old text otherwise. bot_workspaces.last_error_phase is unchanged. Existing
-- rows are not backfilled: a stored message cannot be mapped back to a code
-- reliably.

ALTER TABLE public.schedule_logs
  ADD COLUMN IF NOT EXISTS error_code TEXT NOT NULL DEFAULT '';

ALTER TABLE public.bot_workspaces
  ADD COLUMN IF NOT EXISTS last_error_code TEXT NOT NULL DEFAULT '';
