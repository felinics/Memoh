-- 0133_user_runtime_activation_lifecycle
-- Remove the pending credential lifecycle and restore durable credentials.

ALTER TABLE IF EXISTS public.user_runtimes
  DROP CONSTRAINT IF EXISTS user_runtimes_activation_state_check;
ALTER TABLE IF EXISTS public.user_runtimes
  DROP COLUMN IF EXISTS pending_expires_at;
ALTER TABLE IF EXISTS public.user_runtimes
  DROP COLUMN IF EXISTS activated_at;
