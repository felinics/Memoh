-- 0114_provider_templates
-- Add a global provider template catalog and link tenant-owned provider instances to it.

CREATE SCHEMA IF NOT EXISTS template;

CREATE TABLE IF NOT EXISTS template.provider_templates (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  key TEXT NOT NULL,
  domain TEXT NOT NULL,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  icon TEXT,
  driver TEXT NOT NULL,
  config_schema JSONB NOT NULL DEFAULT '{}'::jsonb,
  default_config JSONB NOT NULL DEFAULT '{}'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  source TEXT NOT NULL DEFAULT '',
  content_hash TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0,
  active BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT provider_templates_domain_key_unique UNIQUE (domain, key),
  CONSTRAINT provider_templates_domain_check CHECK (
    domain IN ('llm', 'speech', 'transcription', 'video')
  )
);

CREATE INDEX IF NOT EXISTS idx_provider_templates_domain_active_order
  ON template.provider_templates (domain, active, sort_order, name);

CREATE TABLE IF NOT EXISTS template.provider_template_models (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_template_id UUID NOT NULL REFERENCES template.provider_templates(id) ON DELETE CASCADE,
  model_id TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  type TEXT NOT NULL DEFAULT 'chat',
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  sort_order INTEGER NOT NULL DEFAULT 0,
  active BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT provider_template_models_identity_unique UNIQUE (provider_template_id, type, model_id),
  CONSTRAINT provider_template_models_type_check CHECK (
    type IN ('chat', 'embedding', 'speech', 'transcription', 'video')
  )
);

CREATE INDEX IF NOT EXISTS idx_provider_template_models_template_active_order
  ON template.provider_template_models (provider_template_id, active, sort_order, model_id);

ALTER TABLE public.providers
  ADD COLUMN IF NOT EXISTS provider_template_id UUID;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
      FROM pg_constraint
     WHERE conname = 'providers_provider_template_id_fkey'
       AND conrelid = 'public.providers'::regclass
  ) THEN
    ALTER TABLE public.providers
      ADD CONSTRAINT providers_provider_template_id_fkey
      FOREIGN KEY (provider_template_id)
      REFERENCES template.provider_templates(id)
      ON DELETE SET NULL (provider_template_id);
  END IF;
END
$$;

CREATE INDEX IF NOT EXISTS idx_providers_provider_template_id
  ON public.providers(team_id, provider_template_id);

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
      FROM pg_constraint con
     WHERE con.conrelid = 'public.search_providers'::regclass
       AND con.contype = 'u'
       AND ARRAY(
         SELECT attr.attname
           FROM unnest(con.conkey) WITH ORDINALITY key(attnum, ord)
           JOIN pg_attribute attr
             ON attr.attrelid = con.conrelid
            AND attr.attnum = key.attnum
          ORDER BY key.ord
       ) = ARRAY['team_id', 'provider']::name[]
  ) THEN
    ALTER TABLE public.search_providers
      ADD CONSTRAINT search_providers_provider_unique
      UNIQUE (team_id, provider);
  END IF;
END
$$;

GRANT USAGE ON SCHEMA template TO CURRENT_USER;
GRANT SELECT, INSERT, UPDATE, DELETE ON
  template.provider_templates,
  template.provider_template_models
TO CURRENT_USER;
