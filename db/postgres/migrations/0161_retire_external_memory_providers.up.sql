-- 0161_retire_external_memory_providers
-- Retire the mem0 and OpenViking memory providers. Both were already no-op
-- placeholders, so a bot that selected one had memory disabled in practice.
-- Clear those selections (memory_provider_id = NULL means memory is off) and
-- delete the placeholder rows. Built-in rows and the table itself are kept.
--
-- bots and memory_providers may use FORCE RLS keyed on memoh.team_id, which a
-- migration connection never sets. Lift the policies for the cleanup and
-- restore each table's original RLS state before leaving the migration.

DO $retire_external_memory_providers$
DECLARE
  bots_rls boolean;
  bots_force boolean;
  providers_rls boolean;
  providers_force boolean;
BEGIN
  IF to_regclass('public.memory_providers') IS NULL OR to_regclass('public.bots') IS NULL THEN
    RETURN;
  END IF;

  SELECT relrowsecurity, relforcerowsecurity INTO bots_rls, bots_force
    FROM pg_class WHERE oid = 'public.bots'::regclass;
  SELECT relrowsecurity, relforcerowsecurity INTO providers_rls, providers_force
    FROM pg_class WHERE oid = 'public.memory_providers'::regclass;

  ALTER TABLE public.bots NO FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bots DISABLE ROW LEVEL SECURITY;
  ALTER TABLE public.memory_providers NO FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.memory_providers DISABLE ROW LEVEL SECURITY;

  UPDATE public.bots
  SET memory_provider_id = NULL,
      updated_at = now()
  WHERE memory_provider_id IN (
    SELECT id FROM public.memory_providers WHERE provider <> 'builtin'
  );

  DELETE FROM public.memory_providers WHERE provider <> 'builtin';

  IF providers_rls THEN
    ALTER TABLE public.memory_providers ENABLE ROW LEVEL SECURITY;
  END IF;
  IF providers_force THEN
    ALTER TABLE public.memory_providers FORCE ROW LEVEL SECURITY;
  END IF;
  IF bots_rls THEN
    ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
  END IF;
  IF bots_force THEN
    ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;
  END IF;
END
$retire_external_memory_providers$;
