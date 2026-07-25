-- 0093_fetch_providers
-- Add configurable web fetch providers and bot-level fetch provider selection.

CREATE TABLE IF NOT EXISTS fetch_providers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  provider TEXT NOT NULL,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  enable BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT fetch_providers_name_unique UNIQUE (name)
);

ALTER TABLE bots
  ADD COLUMN IF NOT EXISTS fetch_provider_id UUID REFERENCES fetch_providers(id) ON DELETE SET NULL;
