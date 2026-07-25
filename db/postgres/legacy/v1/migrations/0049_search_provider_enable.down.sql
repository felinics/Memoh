-- 0049_search_provider_enable (down)
-- Remove the enable column from search_providers.

ALTER TABLE search_providers DROP COLUMN IF EXISTS enable;
