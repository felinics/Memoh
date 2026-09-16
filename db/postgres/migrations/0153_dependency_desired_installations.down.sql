-- 0153_dependency_desired_installations
-- Remove target scheduling; this does not restore deleted dependency payloads.
ALTER TABLE public.bot_dependency_installations
    DROP COLUMN IF EXISTS operation_intent,
    DROP COLUMN IF EXISTS operation_definition_revision,
    DROP COLUMN IF EXISTS operation_registry_id,
    DROP COLUMN IF EXISTS operation_source_url,
    DROP COLUMN IF EXISTS last_operation_id;
DROP TABLE IF EXISTS public.bot_dependency_authorization_events;
DROP TABLE IF EXISTS public.bot_dependency_desired_installations;
