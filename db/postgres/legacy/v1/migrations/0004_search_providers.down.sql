-- 0005_search_providers (down)
ALTER TABLE bots DROP COLUMN IF EXISTS search_provider_id;
DROP TABLE IF EXISTS search_providers;
