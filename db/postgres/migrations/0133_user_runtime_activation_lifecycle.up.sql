-- 0133_user_runtime_activation_lifecycle
-- Expire credentials that never complete a ready Runtime connection.

ALTER TABLE IF EXISTS public.user_runtimes
  ADD COLUMN IF NOT EXISTS activated_at TIMESTAMPTZ;
ALTER TABLE IF EXISTS public.user_runtimes
  ADD COLUMN IF NOT EXISTS pending_expires_at TIMESTAMPTZ;

-- Credentials created before this lifecycle existed are already durable user
-- credentials. Preserve their behavior instead of guessing which historical
-- rows may have completed a connection.
ALTER TABLE public.user_runtimes NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes DISABLE ROW LEVEL SECURITY;

UPDATE public.user_runtimes
SET activated_at = created_at
WHERE activated_at IS NULL
  AND pending_expires_at IS NULL;

ALTER TABLE public.user_runtimes ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes FORCE ROW LEVEL SECURITY;

ALTER TABLE public.user_runtimes
  ALTER COLUMN pending_expires_at SET DEFAULT (now() + INTERVAL '15 minutes');

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conrelid = 'public.user_runtimes'::regclass
      AND conname = 'user_runtimes_activation_state_check'
  ) THEN
    ALTER TABLE public.user_runtimes
      ADD CONSTRAINT user_runtimes_activation_state_check CHECK (
        (activated_at IS NULL AND pending_expires_at IS NOT NULL)
        OR (activated_at IS NOT NULL AND pending_expires_at IS NULL)
      ) NOT VALID;
  END IF;
END $$;

ALTER TABLE public.user_runtimes
  VALIDATE CONSTRAINT user_runtimes_activation_state_check;
