-- 0005_search_providers
-- Add search_providers table and link to bots for web search integration.

CREATE TABLE IF NOT EXISTS search_providers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  provider TEXT NOT NULL,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT search_providers_name_unique UNIQUE (name),
  CONSTRAINT search_providers_provider_check CHECK (provider IN ('brave'))
);

ALTER TABLE bots ADD COLUMN IF NOT EXISTS search_provider_id UUID REFERENCES search_providers(id) ON DELETE SET NULL;
