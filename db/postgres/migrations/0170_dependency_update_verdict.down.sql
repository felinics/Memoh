-- 0170_dependency_update_verdict
-- Remove stored dependency update verdicts.
ALTER TABLE public.bot_dependency_installations
    DROP COLUMN IF EXISTS update_checked_version,
    DROP COLUMN IF EXISTS update_available;
