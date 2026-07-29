-- 0123_explicit_chat_completions_compat
-- Backfill explicit Chat Completions compatibility modes for existing built-in
-- DeepSeek, MiniMax, and Moonshot/Kimi provider instances. Official endpoint
-- URLs match by exact origin or path prefix (covering /v1, /beta, ...), never
-- by substring, so lookalike domains and proxies that merely embed an official
-- hostname are not classified.

ALTER POLICY teams_self_select ON public.teams USING (true);

DO $$
DECLARE
  migration_team_id UUID;
  previous_team_id TEXT;
BEGIN
  previous_team_id := current_setting('memoh.team_id', true);

  FOR migration_team_id IN SELECT id FROM public.teams ORDER BY id LOOP
    PERFORM set_config('memoh.team_id', migration_team_id::text, true);

    WITH provider_identity AS (
      SELECT
        provider.id,
        lower(COALESCE(provider_template.key, '')) AS template_key,
        lower(COALESCE(provider_template.source, '')) AS template_source,
        lower(COALESCE(provider_template.metadata #>> '{preset,id}', '')) AS template_preset_id,
        lower(COALESCE(provider.metadata #>> '{template,key}', '')) AS metadata_template_key,
        lower(COALESCE(provider.metadata #>> '{template,source}', '')) AS metadata_template_source,
        lower(COALESCE(provider.metadata #>> '{preset,id}', '')) AS metadata_preset_id,
        lower(COALESCE(provider.metadata #>> '{preset,source}', '')) AS metadata_preset_source,
        lower(COALESCE(provider.metadata #>> '{registry,source}', '')) AS metadata_registry_source,
        lower(rtrim(btrim(COALESCE(provider.config ->> 'base_url', '')), '/')) AS base_url
      FROM public.providers AS provider
      LEFT JOIN template.provider_templates AS provider_template
        ON provider_template.id = provider.provider_template_id
      WHERE provider.client_type = 'openai-completions'
        AND NULLIF(btrim(provider.config ->> 'chat_completions_compat'), '') IS NULL
    ),
    classified AS (
      SELECT
        id,
        CASE
          WHEN template_key = 'deepseek'
            OR template_preset_id = 'deepseek'
            OR metadata_template_key = 'deepseek'
            OR metadata_preset_id = 'deepseek'
            OR template_source IN ('deepseek.yaml', 'deepseek.yml')
            OR metadata_template_source IN ('deepseek.yaml', 'deepseek.yml')
            OR metadata_preset_source IN ('deepseek.yaml', 'deepseek.yml')
            OR metadata_registry_source IN ('deepseek.yaml', 'deepseek.yml')
            OR base_url = 'https://api.deepseek.com'
            OR base_url LIKE 'https://api.deepseek.com/%'
            THEN 'deepseek'
          WHEN template_key = 'minimax'
            OR template_preset_id = 'minimax'
            OR metadata_template_key = 'minimax'
            OR metadata_preset_id = 'minimax'
            OR template_source IN ('minimax.yaml', 'minimax.yml')
            OR metadata_template_source IN ('minimax.yaml', 'minimax.yml')
            OR metadata_preset_source IN ('minimax.yaml', 'minimax.yml')
            OR metadata_registry_source IN ('minimax.yaml', 'minimax.yml')
            OR base_url = 'https://api.minimax.io'
            OR base_url LIKE 'https://api.minimax.io/%'
            OR base_url = 'https://api.minimaxi.com'
            OR base_url LIKE 'https://api.minimaxi.com/%'
            THEN 'minimax'
          WHEN template_key = 'moonshot'
            OR template_preset_id = 'moonshot'
            OR metadata_template_key = 'moonshot'
            OR metadata_preset_id = 'moonshot'
            OR template_source IN ('moonshot.yaml', 'moonshot.yml')
            OR metadata_template_source IN ('moonshot.yaml', 'moonshot.yml')
            OR metadata_preset_source IN ('moonshot.yaml', 'moonshot.yml')
            OR metadata_registry_source IN ('moonshot.yaml', 'moonshot.yml')
            OR base_url = 'https://api.moonshot.cn'
            OR base_url LIKE 'https://api.moonshot.cn/%'
            OR base_url = 'https://api.moonshot.ai'
            OR base_url LIKE 'https://api.moonshot.ai/%'
            THEN 'kimi'
          ELSE NULL
        END AS compat
      FROM provider_identity
    )
    UPDATE public.providers AS provider
    SET
      config = jsonb_set(
        provider.config,
        '{chat_completions_compat}',
        to_jsonb(classified.compat),
        true
      ),
      metadata = jsonb_set(
        provider.metadata,
        '{_memoh_migration_0123_chat_completions_compat}',
        to_jsonb(classified.compat),
        true
      ),
      updated_at = now()
    FROM classified
    WHERE provider.id = classified.id
      AND classified.compat IS NOT NULL
      AND NULLIF(btrim(provider.config ->> 'chat_completions_compat'), '') IS NULL;
  END LOOP;

  PERFORM set_config('memoh.team_id', COALESCE(previous_team_id, ''), true);
END
$$;

ALTER POLICY teams_self_select ON public.teams
  USING (id = public.memoh_current_team_id());
