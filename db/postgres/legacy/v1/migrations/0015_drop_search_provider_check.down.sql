-- 0015_drop_search_provider_check (down)
-- Restore the original CHECK constraint limiting provider to 'brave'.

ALTER TABLE search_providers ADD CONSTRAINT search_providers_provider_check CHECK (provider IN ('brave'));
