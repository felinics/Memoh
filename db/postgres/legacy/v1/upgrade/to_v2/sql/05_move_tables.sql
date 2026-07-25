-- v1 -> v2 bridge: move tables/views into owner schemas
-- template catalog tables enter model schema.
ALTER TABLE public.team_members SET SCHEMA iam;
ALTER TABLE public.teams SET SCHEMA iam;
ALTER TABLE public.users SET SCHEMA iam;
ALTER TABLE public.bot_acl_rules SET SCHEMA api;
ALTER TABLE public.bot_channel_admins SET SCHEMA api;
ALTER TABLE public.bot_user_grants SET SCHEMA api;
ALTER TABLE public.bots SET SCHEMA api;
ALTER TABLE public.channel_link_codes SET SCHEMA api;
ALTER TABLE public.user_channel_bindings SET SCHEMA api;
ALTER TABLE public.user_channel_identity_bindings SET SCHEMA api;
ALTER TABLE public.fetch_providers SET SCHEMA model;
ALTER TABLE public.model_variants SET SCHEMA model;
ALTER TABLE public.models SET SCHEMA model;
ALTER TABLE public.provider_oauth_tokens SET SCHEMA model;
ALTER TABLE public.providers SET SCHEMA model;
ALTER TABLE public.search_providers SET SCHEMA model;
ALTER TABLE public.tts_models SET SCHEMA model;
ALTER TABLE public.tts_providers SET SCHEMA model;
ALTER TABLE public.user_provider_oauth_tokens SET SCHEMA model;
ALTER TABLE public.bot_storage_bindings SET SCHEMA media;
ALTER TABLE public.media_assets SET SCHEMA media;
ALTER TABLE public.storage_providers SET SCHEMA media;
ALTER TABLE public.bot_heartbeat_logs SET SCHEMA agent;
ALTER TABLE public.bot_history_message_assets SET SCHEMA agent;
ALTER TABLE public.bot_history_message_compacts SET SCHEMA agent;
ALTER TABLE public.bot_history_messages SET SCHEMA agent;
ALTER TABLE public.bot_plugin_installations SET SCHEMA agent;
ALTER TABLE public.bot_plugin_resources SET SCHEMA agent;
ALTER TABLE public.bot_sessions SET SCHEMA agent;
ALTER TABLE public.mcp_connections SET SCHEMA agent;
ALTER TABLE public.mcp_oauth_tokens SET SCHEMA agent;
ALTER TABLE public.schedule SET SCHEMA agent;
ALTER TABLE public.schedule_logs SET SCHEMA agent;
ALTER TABLE public.subagent_configs SET SCHEMA agent;
ALTER TABLE public.tool_approval_requests SET SCHEMA agent;
ALTER TABLE public.user_input_requests SET SCHEMA agent;
ALTER TABLE public.bot_channel_configs SET SCHEMA channel;
ALTER TABLE public.bot_channel_routes SET SCHEMA channel;
ALTER TABLE public.bot_email_bindings SET SCHEMA channel;
ALTER TABLE public.bot_session_discuss_cursors SET SCHEMA channel;
ALTER TABLE public.bot_session_events SET SCHEMA channel;
ALTER TABLE public.channel_identities SET SCHEMA channel;
ALTER TABLE public.email_oauth_tokens SET SCHEMA channel;
ALTER TABLE public.email_outbox SET SCHEMA channel;
ALTER TABLE public.email_providers SET SCHEMA channel;
ALTER TABLE public.memory_edges SET SCHEMA memory;
ALTER TABLE public.memory_nodes SET SCHEMA memory;
ALTER TABLE public.memory_providers SET SCHEMA memory;
ALTER TABLE public.bot_remote_runtime_bindings SET SCHEMA runtime;
ALTER TABLE public.bot_workspace_resource_limits SET SCHEMA runtime;
ALTER TABLE public.container_versions SET SCHEMA runtime;
ALTER TABLE public.containers SET SCHEMA runtime;
ALTER TABLE public.lifecycle_events SET SCHEMA runtime;
ALTER TABLE public.snapshots SET SCHEMA runtime;
ALTER TABLE public.tasks SET SCHEMA runtime;
ALTER TABLE public.user_runtimes SET SCHEMA runtime;
ALTER TABLE template.provider_templates SET SCHEMA model;
ALTER TABLE template.provider_template_models SET SCHEMA model;
ALTER VIEW public.team_accounts SET SCHEMA iam;
ALTER VIEW public.bot_visible_history_messages SET SCHEMA agent;

ALTER TABLE agent.bot_sessions
  ADD COLUMN IF NOT EXISTS conversation_type text,
  ADD COLUMN IF NOT EXISTS conversation_name text,
  ADD COLUMN IF NOT EXISTS reply_target text;

UPDATE agent.bot_sessions AS session
SET
  conversation_type = COALESCE(session.conversation_type, route.conversation_type),
  conversation_name = COALESCE(
    session.conversation_name,
    NULLIF(BTRIM(route.metadata->>'conversation_name'), '')
  ),
  reply_target = COALESCE(session.reply_target, route.default_reply_target)
FROM channel.bot_channel_routes AS route
WHERE route.team_id = session.team_id
  AND route.id = session.route_id
  AND (
    session.conversation_type IS NULL
    OR session.conversation_name IS NULL
    OR session.reply_target IS NULL
  );

ALTER TABLE agent.bot_history_messages
  ADD COLUMN IF NOT EXISTS sender_display_name text,
  ADD COLUMN IF NOT EXISTS sender_avatar_url text;

UPDATE agent.bot_history_messages AS message
SET
  sender_display_name = COALESCE(
    NULLIF(message.sender_display_name, ''),
    (
      SELECT NULLIF(BTRIM(identity.display_name), '')
      FROM channel.channel_identities AS identity
      WHERE identity.team_id = message.team_id
        AND identity.id = message.sender_channel_identity_id
    ),
    (
      SELECT NULLIF(BTRIM(account.display_name), '')
      FROM iam.users AS account
      WHERE account.id = message.sender_account_user_id
    )
  ),
  sender_avatar_url = COALESCE(
    NULLIF(message.sender_avatar_url, ''),
    (
      SELECT NULLIF(BTRIM(identity.avatar_url), '')
      FROM channel.channel_identities AS identity
      WHERE identity.team_id = message.team_id
        AND identity.id = message.sender_channel_identity_id
    ),
    (
      SELECT NULLIF(BTRIM(account.avatar_url), '')
      FROM iam.users AS account
      WHERE account.id = message.sender_account_user_id
    )
  )
WHERE (
    NULLIF(message.sender_display_name, '') IS NULL
    OR NULLIF(message.sender_avatar_url, '') IS NULL
  )
  AND (
    message.sender_channel_identity_id IS NOT NULL
    OR message.sender_account_user_id IS NOT NULL
  );

CREATE OR REPLACE VIEW agent.bot_visible_history_messages
WITH (security_invoker='true') AS
SELECT
  team_id,
  turn_id,
  turn_position,
  turn_message_seq,
  id,
  bot_id,
  session_id,
  sender_channel_identity_id,
  sender_account_user_id,
  source_message_id,
  source_reply_to_message_id,
  role,
  content,
  metadata,
  usage,
  compact_id,
  session_mode,
  runtime_type,
  model_id,
  event_id,
  display_text,
  created_at,
  sender_display_name,
  sender_avatar_url
FROM agent.bot_history_messages
WHERE turn_visible = true
  AND turn_id IS NOT NULL
  AND turn_position IS NOT NULL
  AND turn_message_seq IS NOT NULL;

DROP SCHEMA IF EXISTS template;

ALTER TABLE channel.bot_session_discuss_cursors
  ADD COLUMN IF NOT EXISTS bot_id uuid;

UPDATE channel.bot_session_discuss_cursors AS discuss_cursor
SET bot_id = session.bot_id
FROM agent.bot_sessions AS session
WHERE session.team_id = discuss_cursor.team_id
  AND session.id = discuss_cursor.session_id
  AND discuss_cursor.bot_id IS NULL;

-- Orphan cleanup: cursors referencing a session that no longer exists cannot be
-- backfilled with a bot_id, so they are dropped before the NOT NULL constraint.
DELETE FROM channel.bot_session_discuss_cursors
WHERE bot_id IS NULL;

ALTER TABLE channel.bot_session_discuss_cursors
  ALTER COLUMN bot_id SET NOT NULL;

CREATE INDEX IF NOT EXISTS idx_bot_session_discuss_cursors_bot
  ON channel.bot_session_discuss_cursors USING btree (team_id, bot_id);
