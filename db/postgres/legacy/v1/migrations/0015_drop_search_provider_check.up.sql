-- 0015_drop_search_provider_check
-- Remove the CHECK constraint on search_providers.provider so new providers
-- can be added without a database migration.

ALTER TABLE search_providers DROP CONSTRAINT IF EXISTS search_providers_provider_check;
