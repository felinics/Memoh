-- 0164_bot_dependency_installation_last_error_code
-- bot_dependency_installations.last_error holds English sentences and upstream
-- output written by older servers. New failures store the public catalog code
-- in last_error_code and leave last_error empty; clients render errors.<code>
-- when the code is set and show the old text otherwise. Existing rows are not
-- backfilled: a stored sentence cannot be mapped back to a code reliably.

ALTER TABLE public.bot_dependency_installations
  ADD COLUMN IF NOT EXISTS last_error_code TEXT NOT NULL DEFAULT '';
