-- 0148_bot_dependency_installations
-- Drop dependency caches and installation intent. Their indexes, policies,
-- and outbound foreign keys are removed with the owning tables.

DROP TABLE IF EXISTS public.workspace_dependency_catalogs;
DROP TABLE IF EXISTS public.workspace_dependency_definitions;
DROP TABLE IF EXISTS public.bot_dependency_installations;
