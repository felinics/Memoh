-- 0049_search_provider_enable
-- Add enable column to search_providers table for toggling providers on/off.

ALTER TABLE search_providers ADD COLUMN IF NOT EXISTS enable BOOLEAN NOT NULL DEFAULT false;
