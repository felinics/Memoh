-- 0093_fetch_providers
-- Remove configurable web fetch providers and bot-level fetch provider selection.

ALTER TABLE bots
  DROP COLUMN IF EXISTS fetch_provider_id;

DROP TABLE IF EXISTS fetch_providers;
