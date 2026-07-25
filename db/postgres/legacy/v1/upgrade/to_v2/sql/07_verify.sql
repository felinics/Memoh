-- v1 -> v2 bridge: verify cutover invariants + owner map catalog assert
DO $$
DECLARE
  cross_fk int;
  public_business int;
BEGIN
  SELECT count(*) INTO cross_fk
  FROM pg_constraint c
  JOIN pg_class cs ON cs.oid = c.conrelid
  JOIN pg_namespace ns ON ns.oid = cs.relnamespace
  JOIN pg_class cd ON cd.oid = c.confrelid
  JOIN pg_namespace nd ON nd.oid = cd.relnamespace
  WHERE c.contype = 'f'
    AND ns.nspname IN ('api','agent','channel','memory','runtime','model','media','iam')
    AND nd.nspname IN ('api','agent','channel','memory','runtime','model','media','iam')
    AND ns.nspname IS DISTINCT FROM nd.nspname;
  IF cross_fk <> 0 THEN
    RAISE EXCEPTION 'verify failed: % cross-owner foreign keys remain', cross_fk;
  END IF;

  SELECT count(*) INTO public_business
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname = 'public'
    AND c.relkind = 'r'
    AND c.relname <> 'schema_migrations';
  IF public_business <> 0 THEN
    RAISE EXCEPTION 'verify failed: % unexpected public business tables remain', public_business;
  END IF;

  IF to_regprocedure('iam.memoh_current_team_id()') IS NULL THEN
    RAISE EXCEPTION 'verify failed: iam.memoh_current_team_id missing';
  END IF;
  IF to_regprocedure('iam.memoh_guard_last_active_team_admin()') IS NULL THEN
    RAISE EXCEPTION 'verify failed: iam.memoh_guard_last_active_team_admin missing';
  END IF;
  IF to_regtype('iam.user_role') IS NULL THEN
    RAISE EXCEPTION 'verify failed: iam.user_role missing';
  END IF;
  IF to_regclass('public.schema_migrations') IS NULL THEN
    RAISE EXCEPTION 'verify failed: public.schema_migrations missing';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pgcrypto') THEN
    RAISE EXCEPTION 'verify failed: pgcrypto extension missing';
  END IF;

  IF to_regclass('iam.teams') IS NULL THEN RAISE EXCEPTION 'missing iam.teams'; END IF;
  IF to_regclass('iam.users') IS NULL THEN RAISE EXCEPTION 'missing iam.users'; END IF;
  IF to_regclass('iam.team_members') IS NULL THEN RAISE EXCEPTION 'missing iam.team_members'; END IF;
  IF to_regclass('api.bots') IS NULL THEN RAISE EXCEPTION 'missing api.bots'; END IF;
  IF to_regclass('api.bot_user_grants') IS NULL THEN RAISE EXCEPTION 'missing api.bot_user_grants'; END IF;
  IF to_regclass('api.bot_acl_rules') IS NULL THEN RAISE EXCEPTION 'missing api.bot_acl_rules'; END IF;
  IF to_regclass('api.bot_channel_admins') IS NULL THEN RAISE EXCEPTION 'missing api.bot_channel_admins'; END IF;
  IF to_regclass('api.channel_link_codes') IS NULL THEN RAISE EXCEPTION 'missing api.channel_link_codes'; END IF;
  IF to_regclass('api.user_channel_identity_bindings') IS NULL THEN RAISE EXCEPTION 'missing api.user_channel_identity_bindings'; END IF;
  IF to_regclass('api.user_channel_bindings') IS NULL THEN RAISE EXCEPTION 'missing api.user_channel_bindings'; END IF;
  IF to_regclass('agent.bot_heartbeat_logs') IS NULL THEN RAISE EXCEPTION 'missing agent.bot_heartbeat_logs'; END IF;
  IF to_regclass('agent.bot_history_message_assets') IS NULL THEN RAISE EXCEPTION 'missing agent.bot_history_message_assets'; END IF;
  IF to_regclass('agent.bot_history_message_compacts') IS NULL THEN RAISE EXCEPTION 'missing agent.bot_history_message_compacts'; END IF;
  IF to_regclass('agent.bot_history_messages') IS NULL THEN RAISE EXCEPTION 'missing agent.bot_history_messages'; END IF;
  IF to_regclass('agent.bot_plugin_installations') IS NULL THEN RAISE EXCEPTION 'missing agent.bot_plugin_installations'; END IF;
  IF to_regclass('agent.bot_plugin_resources') IS NULL THEN RAISE EXCEPTION 'missing agent.bot_plugin_resources'; END IF;
  IF to_regclass('agent.bot_sessions') IS NULL THEN RAISE EXCEPTION 'missing agent.bot_sessions'; END IF;
  IF to_regclass('agent.mcp_connections') IS NULL THEN RAISE EXCEPTION 'missing agent.mcp_connections'; END IF;
  IF to_regclass('agent.mcp_oauth_tokens') IS NULL THEN RAISE EXCEPTION 'missing agent.mcp_oauth_tokens'; END IF;
  IF to_regclass('agent.schedule') IS NULL THEN RAISE EXCEPTION 'missing agent.schedule'; END IF;
  IF to_regclass('agent.schedule_logs') IS NULL THEN RAISE EXCEPTION 'missing agent.schedule_logs'; END IF;
  IF to_regclass('agent.subagent_configs') IS NULL THEN RAISE EXCEPTION 'missing agent.subagent_configs'; END IF;
  IF to_regclass('agent.tool_approval_requests') IS NULL THEN RAISE EXCEPTION 'missing agent.tool_approval_requests'; END IF;
  IF to_regclass('agent.user_input_requests') IS NULL THEN RAISE EXCEPTION 'missing agent.user_input_requests'; END IF;
  IF to_regclass('channel.bot_channel_configs') IS NULL THEN RAISE EXCEPTION 'missing channel.bot_channel_configs'; END IF;
  IF to_regclass('channel.bot_channel_routes') IS NULL THEN RAISE EXCEPTION 'missing channel.bot_channel_routes'; END IF;
  IF to_regclass('channel.bot_email_bindings') IS NULL THEN RAISE EXCEPTION 'missing channel.bot_email_bindings'; END IF;
  IF to_regclass('channel.bot_session_discuss_cursors') IS NULL THEN RAISE EXCEPTION 'missing channel.bot_session_discuss_cursors'; END IF;
  IF to_regclass('channel.bot_session_events') IS NULL THEN RAISE EXCEPTION 'missing channel.bot_session_events'; END IF;
  IF to_regclass('channel.channel_identities') IS NULL THEN RAISE EXCEPTION 'missing channel.channel_identities'; END IF;
  IF to_regclass('channel.email_oauth_tokens') IS NULL THEN RAISE EXCEPTION 'missing channel.email_oauth_tokens'; END IF;
  IF to_regclass('channel.email_outbox') IS NULL THEN RAISE EXCEPTION 'missing channel.email_outbox'; END IF;
  IF to_regclass('channel.email_providers') IS NULL THEN RAISE EXCEPTION 'missing channel.email_providers'; END IF;
  IF to_regclass('memory.memory_edges') IS NULL THEN RAISE EXCEPTION 'missing memory.memory_edges'; END IF;
  IF to_regclass('memory.memory_nodes') IS NULL THEN RAISE EXCEPTION 'missing memory.memory_nodes'; END IF;
  IF to_regclass('memory.memory_providers') IS NULL THEN RAISE EXCEPTION 'missing memory.memory_providers'; END IF;
  IF to_regclass('runtime.bot_remote_runtime_bindings') IS NULL THEN RAISE EXCEPTION 'missing runtime.bot_remote_runtime_bindings'; END IF;
  IF to_regclass('runtime.bot_workspace_resource_limits') IS NULL THEN RAISE EXCEPTION 'missing runtime.bot_workspace_resource_limits'; END IF;
  IF to_regclass('runtime.container_versions') IS NULL THEN RAISE EXCEPTION 'missing runtime.container_versions'; END IF;
  IF to_regclass('runtime.containers') IS NULL THEN RAISE EXCEPTION 'missing runtime.containers'; END IF;
  IF to_regclass('runtime.lifecycle_events') IS NULL THEN RAISE EXCEPTION 'missing runtime.lifecycle_events'; END IF;
  IF to_regclass('runtime.snapshots') IS NULL THEN RAISE EXCEPTION 'missing runtime.snapshots'; END IF;
  IF to_regclass('runtime.user_runtimes') IS NULL THEN RAISE EXCEPTION 'missing runtime.user_runtimes'; END IF;
  IF to_regclass('runtime.tasks') IS NULL THEN RAISE EXCEPTION 'missing runtime.tasks'; END IF;
  IF to_regclass('model.fetch_providers') IS NULL THEN RAISE EXCEPTION 'missing model.fetch_providers'; END IF;
  IF to_regclass('model.model_variants') IS NULL THEN RAISE EXCEPTION 'missing model.model_variants'; END IF;
  IF to_regclass('model.models') IS NULL THEN RAISE EXCEPTION 'missing model.models'; END IF;
  IF to_regclass('model.provider_oauth_tokens') IS NULL THEN RAISE EXCEPTION 'missing model.provider_oauth_tokens'; END IF;
  IF to_regclass('model.providers') IS NULL THEN RAISE EXCEPTION 'missing model.providers'; END IF;
  IF to_regclass('model.search_providers') IS NULL THEN RAISE EXCEPTION 'missing model.search_providers'; END IF;
  IF to_regclass('model.tts_models') IS NULL THEN RAISE EXCEPTION 'missing model.tts_models'; END IF;
  IF to_regclass('model.tts_providers') IS NULL THEN RAISE EXCEPTION 'missing model.tts_providers'; END IF;
  IF to_regclass('model.user_provider_oauth_tokens') IS NULL THEN RAISE EXCEPTION 'missing model.user_provider_oauth_tokens'; END IF;
  IF to_regclass('model.provider_templates') IS NULL THEN RAISE EXCEPTION 'missing model.provider_templates'; END IF;
  IF to_regclass('model.provider_template_models') IS NULL THEN RAISE EXCEPTION 'missing model.provider_template_models'; END IF;
  IF to_regclass('media.bot_storage_bindings') IS NULL THEN RAISE EXCEPTION 'missing media.bot_storage_bindings'; END IF;
  IF to_regclass('media.media_assets') IS NULL THEN RAISE EXCEPTION 'missing media.media_assets'; END IF;
  IF to_regclass('media.storage_providers') IS NULL THEN RAISE EXCEPTION 'missing media.storage_providers'; END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'channel'
      AND table_name = 'bot_session_discuss_cursors'
      AND column_name = 'bot_id'
      AND is_nullable = 'NO'
  ) THEN
    RAISE EXCEPTION 'verify failed: channel.bot_session_discuss_cursors.bot_id missing or nullable';
  END IF;

  -- exact owned table count per owner schema
  IF (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam' AND c.relkind='r') <> 3 THEN
    RAISE EXCEPTION 'iam table count mismatch';
  END IF;
  IF (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='api' AND c.relkind='r') <> 7 THEN
    RAISE EXCEPTION 'api table count mismatch';
  END IF;
  IF (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='agent' AND c.relkind='r') <> 14 THEN
    RAISE EXCEPTION 'agent table count mismatch';
  END IF;
  IF (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='channel' AND c.relkind='r') <> 9 THEN
    RAISE EXCEPTION 'channel table count mismatch';
  END IF;
  IF (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='memory' AND c.relkind='r') <> 3 THEN
    RAISE EXCEPTION 'memory table count mismatch';
  END IF;
  IF (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='runtime' AND c.relkind='r') <> 8 THEN
    RAISE EXCEPTION 'runtime table count mismatch';
  END IF;
  IF (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='model' AND c.relkind='r') <> 11 THEN
    RAISE EXCEPTION 'model table count mismatch';
  END IF;
  IF (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='media' AND c.relkind='r') <> 3 THEN
    RAISE EXCEPTION 'media table count mismatch';
  END IF;
END $$;
