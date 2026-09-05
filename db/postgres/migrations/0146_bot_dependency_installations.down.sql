-- 0146_bot_dependency_installations
-- Remove the Workspace dependency installation records. Policies, the index,
-- and the bots reference are dropped together with the table.

DROP POLICY IF EXISTS bot_dependency_installations_team_delete ON public.bot_dependency_installations;
DROP POLICY IF EXISTS bot_dependency_installations_team_update ON public.bot_dependency_installations;
DROP POLICY IF EXISTS bot_dependency_installations_team_insert ON public.bot_dependency_installations;
DROP POLICY IF EXISTS bot_dependency_installations_team_select ON public.bot_dependency_installations;

DROP INDEX IF EXISTS public.idx_bot_dependency_installations_bot;

ALTER TABLE IF EXISTS public.bot_dependency_installations
    DROP CONSTRAINT IF EXISTS bot_dependency_installations_bot_id_fkey;

DROP TABLE IF EXISTS public.bot_dependency_installations;
