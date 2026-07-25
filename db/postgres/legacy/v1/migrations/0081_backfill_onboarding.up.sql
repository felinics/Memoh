-- Backfill existing users so they skip onboarding.
-- Tag backfilled rows with `onboarding_backfilled` so the down migration can roll
-- back ONLY what we wrote here, not real onboarding completions made afterward.
-- New users created after this migration have metadata='{}' (no onboarding_completed
-- key) and will be shown the onboarding flow.
--
-- This is a single-statement UPDATE and golang-migrate runs it in one transaction,
-- which can hold locks on a large users table. On big self-hosted instances, run
-- the equivalent in batches BEFORE applying this migration, e.g.:
--   UPDATE users SET metadata = jsonb_set(jsonb_set(COALESCE(metadata,'{}'),
--       '{onboarding_completed}','true'), '{onboarding_backfilled}','true')
--   WHERE metadata->>'onboarding_completed' IS NULL
--     AND id IN (SELECT id FROM users WHERE metadata->>'onboarding_completed' IS NULL LIMIT 1000);
UPDATE users
SET metadata = jsonb_set(
  jsonb_set(COALESCE(metadata, '{}'), '{onboarding_completed}', 'true'),
  '{onboarding_backfilled}', 'true'
)
WHERE metadata->>'onboarding_completed' IS NULL;
