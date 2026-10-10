-- 0164_bot_dependency_installation_last_error_code (down)
ALTER TABLE public.bot_dependency_installations
  DROP COLUMN IF EXISTS last_error_code;
