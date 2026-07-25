-- 0114_provider_templates
-- Remove the global provider template catalog and its provider instance link.

ALTER TABLE IF EXISTS public.search_providers
  DROP CONSTRAINT IF EXISTS search_providers_provider_unique;

ALTER TABLE IF EXISTS public.search_providers
  DROP CONSTRAINT IF EXISTS search_providers_team_provider_unique;

DROP INDEX IF EXISTS public.idx_providers_provider_template_id;

ALTER TABLE IF EXISTS public.providers
  DROP COLUMN IF EXISTS provider_template_id;

DROP TABLE IF EXISTS template.provider_template_models;
DROP TABLE IF EXISTS template.provider_templates;
DROP SCHEMA IF EXISTS template;
