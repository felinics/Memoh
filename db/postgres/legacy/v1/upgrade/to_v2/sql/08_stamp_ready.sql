-- v1 -> v2 bridge: stamp readiness marker
-- Goose owner version tables are created by the unified Migrator (not hand-built here).
DO $$
BEGIN
  RAISE NOTICE 'epoch v2 bridge cutover SQL complete; Migrator may stamp owner baselines at version 1';
END $$;
