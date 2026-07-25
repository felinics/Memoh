-- v1 -> v2 bridge: preflight
-- Requires public.schema_migrations at version 119, non-dirty.
DO $$
DECLARE
  ver bigint;
  is_dirty boolean;
BEGIN
  SELECT m.version, m.dirty INTO ver, is_dirty FROM public.schema_migrations AS m;
  IF ver IS DISTINCT FROM 119 THEN
    RAISE EXCEPTION 'epoch v2 bridge requires schema_migrations.version=119, got %', ver;
  END IF;
  IF is_dirty THEN
    RAISE EXCEPTION 'epoch v2 bridge refuses dirty schema_migrations';
  END IF;
END $$;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_namespace
     WHERE nspname IN ('iam','api','agent','channel','memory','runtime','model','media')
  ) THEN
    RAISE EXCEPTION 'owner schemas already present; refuse blind re-bridge';
  END IF;
END $$;
