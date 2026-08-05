-- 0124_connect_it
-- Remove bot-scoped Connect-It connection bindings. The connect_it schema is
-- owned and migrated by Connect-It itself, so it is never touched here.

DROP TABLE IF EXISTS public.connectors;
