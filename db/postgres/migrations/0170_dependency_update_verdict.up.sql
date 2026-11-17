-- 0170_dependency_update_verdict
-- Persist recipe verdicts with the installed version they checked.
ALTER TABLE public.bot_dependency_installations
    ADD COLUMN IF NOT EXISTS update_available BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS update_checked_version TEXT NOT NULL DEFAULT '';
