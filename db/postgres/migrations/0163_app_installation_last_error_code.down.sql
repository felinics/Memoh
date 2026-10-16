-- 0163_app_installation_last_error_code (down)
ALTER TABLE public.bot_app_installations
  DROP COLUMN IF EXISTS last_error_code;
