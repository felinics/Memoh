-- Only roll back rows this migration backfilled (tagged onboarding_backfilled),
-- never real onboarding completions a user made after the backfill ran.
UPDATE users
SET metadata = metadata - 'onboarding_completed' - 'onboarding_backfilled'
WHERE metadata->>'onboarding_backfilled' = 'true';
