CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Global provider catalog. These rows describe reusable product templates and
-- intentionally have no team_id or tenant RLS policy.
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

GRANT USAGE ON SCHEMA template TO CURRENT_USER;
GRANT SELECT, INSERT, UPDATE, DELETE ON
  template.provider_templates,
  template.provider_template_models
TO CURRENT_USER;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'user_role') THEN
    CREATE TYPE user_role AS ENUM ('member', 'admin');
  END IF;
END
$$;

-- users: Memoh user principal
CREATE TABLE IF NOT EXISTS users (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  username TEXT,
  email TEXT,
  password_hash TEXT,
  role user_role NOT NULL DEFAULT 'member',
  display_name TEXT,
  avatar_url TEXT,
  timezone TEXT NOT NULL DEFAULT 'UTC',
  data_root TEXT,
  last_login_at TIMESTAMPTZ,
  is_active BOOLEAN NOT NULL DEFAULT true,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT users_email_unique UNIQUE (email),
  CONSTRAINT users_username_unique UNIQUE (username)
);

-- channel_identities: unified inbound identity subject
CREATE TABLE IF NOT EXISTS channel_identities (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  channel_type TEXT NOT NULL,
  channel_subject_id TEXT NOT NULL,
  display_name TEXT,
  avatar_url TEXT,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT channel_identities_channel_type_subject_unique UNIQUE (channel_type, channel_subject_id)
);

CREATE INDEX IF NOT EXISTS idx_channel_identities_user_id ON channel_identities(user_id);

-- user_channel_bindings: outbound delivery config
CREATE TABLE IF NOT EXISTS user_channel_bindings (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  channel_type TEXT NOT NULL,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT user_channel_bindings_unique UNIQUE (user_id, channel_type)
);

CREATE INDEX IF NOT EXISTS idx_user_channel_bindings_user_id ON user_channel_bindings(user_id);

CREATE TABLE IF NOT EXISTS providers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_template_id UUID,
  name TEXT NOT NULL,
  client_type TEXT NOT NULL DEFAULT 'openai-completions',
  icon TEXT,
  enable BOOLEAN NOT NULL DEFAULT true,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT providers_provider_template_id_fkey
    FOREIGN KEY (provider_template_id)
    REFERENCES template.provider_templates(id)
    ON DELETE SET NULL (provider_template_id),
  CONSTRAINT providers_name_unique UNIQUE (name),
  CONSTRAINT providers_client_type_check CHECK (client_type IN (
    'openai-responses',
    'openai-completions',
    'anthropic-messages',
    'google-generative-ai',
    'openai-codex',
    'github-copilot',
    'edge-speech',
    'openai-speech',
    'openai-transcription',
    'openrouter-speech',
    'openrouter-transcription',
    'elevenlabs-speech',
    'elevenlabs-transcription',
    'deepgram-speech',
    'deepgram-transcription',
    'minimax-speech',
    'volcengine-speech',
    'alibabacloud-speech',
    'microsoft-speech',
    'google-speech',
    'google-transcription',
    'openrouter-video',
    'modelark-video',
    'volcengine-video'
  ))
);

CREATE TABLE IF NOT EXISTS search_providers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  provider TEXT NOT NULL,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  enable BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT search_providers_name_unique UNIQUE (name),
  CONSTRAINT search_providers_provider_unique UNIQUE (provider)
);

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

CREATE TABLE IF NOT EXISTS models (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  model_id TEXT NOT NULL,
  name TEXT,
  provider_id UUID NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  type TEXT NOT NULL DEFAULT 'chat',
  enable BOOLEAN NOT NULL DEFAULT true,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT models_provider_id_model_id_unique UNIQUE (provider_id, model_id),
  CONSTRAINT models_type_check CHECK (type IN ('chat', 'embedding', 'speech', 'transcription', 'video'))
);

CREATE TABLE IF NOT EXISTS model_variants (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  model_uuid UUID NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  variant_id TEXT NOT NULL,
  weight INTEGER NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_model_variants_model_uuid ON model_variants(model_uuid);
CREATE INDEX IF NOT EXISTS idx_model_variants_variant_id ON model_variants(variant_id);

CREATE TABLE IF NOT EXISTS memory_providers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  provider TEXT NOT NULL,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  is_default BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT memory_providers_name_unique UNIQUE (name)
);

CREATE TABLE IF NOT EXISTS bots (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  type TEXT NOT NULL,
  name TEXT NOT NULL,
  display_name TEXT,
  avatar_url TEXT,
  timezone TEXT,
  is_active BOOLEAN NOT NULL DEFAULT true,
  status TEXT NOT NULL DEFAULT 'ready',
  language TEXT NOT NULL DEFAULT 'auto',
  command_ui_language TEXT NOT NULL DEFAULT 'auto',
  reasoning_enabled BOOLEAN NOT NULL DEFAULT false,
  reasoning_effort TEXT NOT NULL DEFAULT 'medium',
  chat_model_id UUID REFERENCES models(id) ON DELETE SET NULL,
  chat_runtime TEXT NOT NULL DEFAULT 'model' CHECK (chat_runtime IN ('model', 'acp_agent')),
  chat_acp_agent_id TEXT,
  chat_acp_project_path TEXT NOT NULL DEFAULT '/data',
  chat_acp_project_mode TEXT NOT NULL DEFAULT 'project' CHECK (chat_acp_project_mode IN ('project', 'none')),
  search_provider_id UUID REFERENCES search_providers(id) ON DELETE SET NULL,
  fetch_provider_id UUID REFERENCES fetch_providers(id) ON DELETE SET NULL,
  memory_provider_id UUID REFERENCES memory_providers(id) ON DELETE SET NULL,
  heartbeat_enabled BOOLEAN NOT NULL DEFAULT false,
  heartbeat_interval INTEGER NOT NULL DEFAULT 1440,
  heartbeat_prompt TEXT NOT NULL DEFAULT '',
  heartbeat_model_id UUID REFERENCES models(id) ON DELETE SET NULL,
  compaction_enabled BOOLEAN NOT NULL DEFAULT false,
  compaction_threshold INTEGER NOT NULL DEFAULT 100000,
  compaction_ratio INTEGER NOT NULL DEFAULT 80,
  compaction_model_id UUID REFERENCES models(id) ON DELETE SET NULL,
  image_model_id UUID REFERENCES models(id) ON DELETE SET NULL,
  discuss_probe_model_id UUID REFERENCES models(id) ON DELETE SET NULL,
  tts_model_id UUID REFERENCES models(id) ON DELETE SET NULL,
  transcription_model_id UUID REFERENCES models(id) ON DELETE SET NULL,
  video_model_id UUID REFERENCES models(id) ON DELETE SET NULL,
  persist_full_tool_results BOOLEAN NOT NULL DEFAULT false,
  show_tool_calls_in_im BOOLEAN NOT NULL DEFAULT false,
  tool_approval_config JSONB NOT NULL DEFAULT '{"enabled":false,"read":{"require_approval":false,"bypass_globs":[],"force_review_globs":[]},"write":{"require_approval":true,"bypass_globs":["/data/**","/tmp/**"],"force_review_globs":[]},"exec":{"require_approval":false,"bypass_commands":[],"force_review_commands":[]}}'::jsonb,
  display_enabled BOOLEAN NOT NULL DEFAULT false,
  overlay_provider TEXT NOT NULL DEFAULT '',
  overlay_enabled BOOLEAN NOT NULL DEFAULT false,
  overlay_config JSONB NOT NULL DEFAULT '{}'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  acl_default_effect TEXT NOT NULL DEFAULT 'allow',
  CONSTRAINT bots_type_check CHECK (type IN ('personal', 'public')),
  CONSTRAINT bots_status_check CHECK (status IN ('creating', 'ready', 'deleting')),
  CONSTRAINT bots_acl_default_effect_check CHECK (acl_default_effect IN ('allow', 'deny')),
  -- reasoning_effort is a free-form capability-driven tier string; no CHECK constraint (see 0093).
  CONSTRAINT bots_name_format_check CHECK (name ~ '^[a-z0-9][a-z0-9-]{1,62}$')
);

CREATE INDEX IF NOT EXISTS idx_bots_owner_user_id ON bots(owner_user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_bots_name ON bots(name);

-- user_runtimes: API tokens for Remote Runtime WebSocket clients. Tokens stay
-- readable so an authenticated owner can copy the connection command again.
CREATE TABLE IF NOT EXISTS user_runtimes (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL CHECK (btrim(name) <> ''),
  api_token TEXT NOT NULL UNIQUE,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_runtimes_active_user_name
  ON user_runtimes(user_id, lower(name))
  WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_user_runtimes_user_id ON user_runtimes(user_id);

-- bot_remote_runtime_bindings: persistent Bot workspace placement on a
-- user-owned Remote Runtime. The Runtime exposes its host filesystem and uses
-- the OS user's home directory as the default working directory.
CREATE TABLE IF NOT EXISTS bot_remote_runtime_bindings (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  runtime_id UUID NOT NULL REFERENCES user_runtimes(id) ON DELETE RESTRICT,
  is_primary BOOLEAN NOT NULL DEFAULT false,
  tool_approval_config JSONB NOT NULL DEFAULT '{"enabled":true,"read":{"mode":"allow","bypass_globs":[],"force_review_globs":[]},"write":{"mode":"ask","bypass_globs":[],"force_review_globs":[]},"exec":{"mode":"ask","bypass_commands":[],"force_review_commands":[]}}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (bot_id, runtime_id)
);

CREATE INDEX IF NOT EXISTS idx_bot_remote_runtime_bindings_runtime_id
  ON bot_remote_runtime_bindings(runtime_id);
CREATE INDEX IF NOT EXISTS idx_bot_remote_runtime_bindings_bot_id
  ON bot_remote_runtime_bindings(bot_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_remote_runtime_bindings_primary
  ON bot_remote_runtime_bindings(bot_id)
  WHERE is_primary = TRUE;

CREATE TABLE IF NOT EXISTS bot_acl_rules (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  action TEXT NOT NULL,
  effect TEXT NOT NULL,
  subject_kind TEXT NOT NULL,
  user_id UUID REFERENCES users(id) ON DELETE CASCADE,
  channel_identity_id UUID REFERENCES channel_identities(id) ON DELETE CASCADE,
  source_channel TEXT,
  source_conversation_type TEXT,
  source_conversation_id TEXT,
  source_thread_id TEXT,
  created_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  enabled BOOLEAN NOT NULL DEFAULT true,
  description TEXT,
  subject_channel_type TEXT,
  CONSTRAINT bot_acl_rules_action_check CHECK (action IN ('chat.trigger')),
  CONSTRAINT bot_acl_rules_effect_check CHECK (effect IN ('allow', 'deny')),
  CONSTRAINT bot_acl_rules_subject_kind_check CHECK (subject_kind IN ('guest_all', 'user', 'channel_identity')),
  CONSTRAINT bot_acl_rules_source_conversation_type_check CHECK (
    source_conversation_type IS NULL OR source_conversation_type IN ('private', 'group', 'thread')
  ),
  CONSTRAINT bot_acl_rules_source_scope_check CHECK (
    (source_conversation_id IS NULL AND source_thread_id IS NULL)
    OR source_channel IS NOT NULL
  ),
  CONSTRAINT bot_acl_rules_source_thread_check CHECK (
    source_thread_id IS NULL OR source_conversation_id IS NOT NULL
  ),
  CONSTRAINT bot_acl_rules_subject_value_check CHECK (
    (subject_kind = 'guest_all' AND user_id IS NULL AND channel_identity_id IS NULL) OR
    (subject_kind = 'user' AND user_id IS NOT NULL AND channel_identity_id IS NULL) OR
    (subject_kind = 'channel_identity' AND user_id IS NULL AND channel_identity_id IS NOT NULL)
  ),
  CONSTRAINT bot_acl_rules_unique_user UNIQUE NULLS NOT DISTINCT (
    bot_id, action, effect, subject_kind, user_id,
    source_channel, source_conversation_type, source_conversation_id, source_thread_id
  ),
  CONSTRAINT bot_acl_rules_unique_channel_identity UNIQUE NULLS NOT DISTINCT (
    bot_id, action, effect, subject_kind, channel_identity_id,
    source_channel, source_conversation_type, source_conversation_id, source_thread_id
  )
);

CREATE INDEX IF NOT EXISTS idx_bot_acl_rules_bot_id ON bot_acl_rules(bot_id);
CREATE INDEX IF NOT EXISTS idx_bot_acl_rules_user_id ON bot_acl_rules(user_id);
CREATE INDEX IF NOT EXISTS idx_bot_acl_rules_channel_identity_id ON bot_acl_rules(channel_identity_id);

-- bot_channel_admins: channel-identity level manage grant (run owner-only slash commands)
CREATE TABLE IF NOT EXISTS bot_channel_admins (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  channel_identity_id UUID NOT NULL REFERENCES channel_identities(id) ON DELETE CASCADE,
  granted BOOLEAN NOT NULL DEFAULT true,
  created_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT bot_channel_admins_unique UNIQUE (bot_id, channel_identity_id)
);

CREATE INDEX IF NOT EXISTS idx_bot_channel_admins_bot_id ON bot_channel_admins(bot_id);
CREATE INDEX IF NOT EXISTS idx_bot_channel_admins_channel_identity_id ON bot_channel_admins(channel_identity_id);

-- user_channel_identity_bindings: global account-level link between a web user and
-- an IM channel identity. Lets a member's bot permissions flow to their IM identity.
CREATE TABLE IF NOT EXISTS user_channel_identity_bindings (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  channel_identity_id UUID NOT NULL REFERENCES channel_identities(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT user_channel_identity_bindings_unique UNIQUE (user_id, channel_identity_id)
);

CREATE INDEX IF NOT EXISTS idx_user_channel_identity_bindings_user_id ON user_channel_identity_bindings(user_id);
CREATE INDEX IF NOT EXISTS idx_user_channel_identity_bindings_channel_identity_id ON user_channel_identity_bindings(channel_identity_id);

-- channel_link_codes: one-time codes a user generates in the web app, then sends as
-- /link <code> to a bot in IM to bind the sending channel identity to their account.
CREATE TABLE IF NOT EXISTS channel_link_codes (
  token TEXT PRIMARY KEY,
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  channel_type TEXT NOT NULL DEFAULT '',
  expires_at TIMESTAMPTZ NOT NULL,
  consumed_at TIMESTAMPTZ,
  consumed_channel_identity_id UUID REFERENCES channel_identities(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_channel_link_codes_user_id ON channel_link_codes(user_id);

CREATE TABLE IF NOT EXISTS bot_plugin_installations (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  plugin_id TEXT NOT NULL,
  plugin_name TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'ready',
  enabled BOOLEAN NOT NULL DEFAULT true,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  manifest JSONB NOT NULL DEFAULT '{}'::jsonb,
  installed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT bot_plugin_installations_unique UNIQUE (bot_id, plugin_id)
);

CREATE INDEX IF NOT EXISTS idx_bot_plugin_installations_bot_id ON bot_plugin_installations(bot_id);
CREATE INDEX IF NOT EXISTS idx_bot_plugin_installations_plugin_id ON bot_plugin_installations(plugin_id);

CREATE TABLE IF NOT EXISTS mcp_connections (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  type TEXT NOT NULL,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  is_active BOOLEAN NOT NULL DEFAULT true,
  status TEXT NOT NULL DEFAULT 'unknown',
  tools_cache JSONB NOT NULL DEFAULT '[]'::jsonb,
  last_probed_at TIMESTAMPTZ,
  status_message TEXT NOT NULL DEFAULT '',
  auth_type TEXT NOT NULL DEFAULT 'none',
  managed_by_plugin_installation_id UUID REFERENCES bot_plugin_installations(id) ON DELETE SET NULL,
  managed_resource_key TEXT NOT NULL DEFAULT '',
  visible BOOLEAN NOT NULL DEFAULT true,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT mcp_connections_type_check CHECK (type IN ('stdio', 'http', 'sse')),
  CONSTRAINT mcp_connections_unique UNIQUE (bot_id, name)
);

CREATE INDEX IF NOT EXISTS idx_mcp_connections_bot_id ON mcp_connections(bot_id);
CREATE INDEX IF NOT EXISTS idx_mcp_connections_plugin_installation_id ON mcp_connections(managed_by_plugin_installation_id);

CREATE TABLE IF NOT EXISTS bot_plugin_resources (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  installation_id UUID NOT NULL REFERENCES bot_plugin_installations(id) ON DELETE CASCADE,
  resource_type TEXT NOT NULL,
  resource_key TEXT NOT NULL,
  resource_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT bot_plugin_resources_unique UNIQUE (installation_id, resource_type, resource_key)
);

CREATE INDEX IF NOT EXISTS idx_bot_plugin_resources_installation_id ON bot_plugin_resources(installation_id);
CREATE INDEX IF NOT EXISTS idx_bot_plugin_resources_resource ON bot_plugin_resources(resource_type, resource_id);

CREATE TABLE IF NOT EXISTS mcp_oauth_tokens (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  connection_id UUID NOT NULL UNIQUE REFERENCES mcp_connections(id) ON DELETE CASCADE,
  resource_metadata_url TEXT NOT NULL DEFAULT '',
  authorization_server_url TEXT NOT NULL DEFAULT '',
  authorization_endpoint TEXT NOT NULL DEFAULT '',
  token_endpoint TEXT NOT NULL DEFAULT '',
  registration_endpoint TEXT NOT NULL DEFAULT '',
  scopes_supported TEXT[] NOT NULL DEFAULT '{}',
  client_id TEXT NOT NULL DEFAULT '',
  client_secret TEXT NOT NULL DEFAULT '',
  access_token TEXT NOT NULL DEFAULT '',
  refresh_token TEXT NOT NULL DEFAULT '',
  token_type TEXT NOT NULL DEFAULT 'Bearer',
  expires_at TIMESTAMPTZ,
  scope TEXT NOT NULL DEFAULT '',
  pkce_code_verifier TEXT NOT NULL DEFAULT '',
  state_param TEXT NOT NULL DEFAULT '',
  resource_uri TEXT NOT NULL DEFAULT '',
  redirect_uri TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_mcp_oauth_tokens_connection_id ON mcp_oauth_tokens(connection_id);

-- Bot history is bot-scoped (one history container per bot).

CREATE TABLE IF NOT EXISTS bot_channel_configs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  channel_type TEXT NOT NULL,
  credentials JSONB NOT NULL DEFAULT '{}'::jsonb,
  external_identity TEXT,
  self_identity JSONB NOT NULL DEFAULT '{}'::jsonb,
  routing JSONB NOT NULL DEFAULT '{}'::jsonb,
  capabilities JSONB NOT NULL DEFAULT '{}'::jsonb,
  disabled BOOLEAN NOT NULL DEFAULT false,
  verified_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT bot_channel_unique UNIQUE (bot_id, channel_type)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_channel_external_identity
  ON bot_channel_configs(channel_type, external_identity);

CREATE INDEX IF NOT EXISTS idx_bot_channel_bot_id ON bot_channel_configs(bot_id);

-- channel_identity_bind_codes: one-time codes for channel identity->user linking
CREATE TABLE IF NOT EXISTS channel_identity_bind_codes (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  token TEXT NOT NULL,
  issued_by_user_id UUID NOT NULL REFERENCES users(id),
  channel_type TEXT,
  expires_at TIMESTAMPTZ,
  used_at TIMESTAMPTZ,
  used_by_channel_identity_id UUID REFERENCES channel_identities(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT channel_identity_bind_codes_token_unique UNIQUE (token)
);

CREATE INDEX IF NOT EXISTS idx_channel_identity_bind_codes_channel_type ON channel_identity_bind_codes(channel_type);

-- bot_channel_routes: route mapping for inbound channel threads to bot history.
CREATE TABLE IF NOT EXISTS bot_channel_routes (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  channel_type TEXT NOT NULL,
  channel_config_id UUID REFERENCES bot_channel_configs(id) ON DELETE SET NULL,
  external_conversation_id TEXT NOT NULL,
  external_thread_id TEXT,
  conversation_type TEXT,
  default_reply_target TEXT,
  active_session_id UUID,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_channel_routes_unique
  ON bot_channel_routes (bot_id, channel_type, external_conversation_id, COALESCE(external_thread_id, ''));
CREATE INDEX IF NOT EXISTS idx_bot_channel_routes_bot ON bot_channel_routes(bot_id);

-- bot_sessions: chat sessions within a bot, optionally linked to a channel route.
CREATE SEQUENCE IF NOT EXISTS session_runtime_fencing_token_seq AS BIGINT NO CYCLE;

CREATE TABLE IF NOT EXISTS bot_sessions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  route_id UUID REFERENCES bot_channel_routes(id) ON DELETE SET NULL,
  channel_type TEXT,
  type TEXT NOT NULL DEFAULT 'chat' CHECK (type IN ('chat', 'heartbeat', 'schedule', 'subagent', 'discuss', 'acp_agent')),
  session_mode TEXT NOT NULL DEFAULT 'chat' CHECK (session_mode IN ('chat', 'discuss', 'heartbeat', 'schedule', 'subagent')),
  runtime_type TEXT NOT NULL DEFAULT 'model' CHECK (runtime_type IN ('model', 'acp_agent')),
  runtime_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  title TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  next_turn_position BIGINT NOT NULL DEFAULT 1,
  compaction_epoch BIGINT NOT NULL DEFAULT 0,
  runtime_fencing_token BIGINT NOT NULL DEFAULT 0 CHECK (runtime_fencing_token >= 0),
  parent_session_id UUID REFERENCES bot_sessions(id) ON DELETE SET NULL,
  created_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_bot_sessions_bot_id ON bot_sessions(bot_id);
CREATE INDEX IF NOT EXISTS idx_bot_sessions_route_id ON bot_sessions(route_id);
CREATE INDEX IF NOT EXISTS idx_bot_sessions_bot_active ON bot_sessions(bot_id, deleted_at);
CREATE INDEX IF NOT EXISTS idx_bot_sessions_parent ON bot_sessions(parent_session_id) WHERE parent_session_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_bot_sessions_created_by_user_id ON bot_sessions(created_by_user_id) WHERE created_by_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_bot_sessions_bot_created_by ON bot_sessions(bot_id, created_by_user_id, deleted_at);
CREATE INDEX IF NOT EXISTS idx_bot_sessions_bot_active_updated ON bot_sessions(bot_id, updated_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_bot_sessions_bot_mode_runtime_active_updated
  ON bot_sessions(bot_id, session_mode, runtime_type, updated_at DESC, id DESC)
  WHERE deleted_at IS NULL;

-- Add FK from routes to sessions (deferred to avoid circular dependency during CREATE).
ALTER TABLE bot_channel_routes
  ADD CONSTRAINT fk_bot_channel_routes_active_session
  FOREIGN KEY (active_session_id) REFERENCES bot_sessions(id) ON DELETE SET NULL;

-- bot_session_events: DCP pipeline event store for cold-start replay.
CREATE TABLE IF NOT EXISTS bot_session_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  session_id UUID NOT NULL REFERENCES bot_sessions(id) ON DELETE CASCADE,
  event_kind TEXT NOT NULL CHECK (event_kind IN ('message', 'edit', 'delete', 'service')),
  event_data JSONB NOT NULL,
  external_message_id TEXT,
  sender_channel_identity_id UUID,
  received_at_ms BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_session_events_session_received
  ON bot_session_events (session_id, received_at_ms);
CREATE UNIQUE INDEX IF NOT EXISTS idx_session_events_dedup
  ON bot_session_events (session_id, event_kind, external_message_id)
  WHERE external_message_id IS NOT NULL AND external_message_id != '';

-- bot_history_messages: unified message history under bot scope.
CREATE TABLE IF NOT EXISTS bot_history_messages (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  session_id UUID REFERENCES bot_sessions(id) ON DELETE SET NULL,
  sender_channel_identity_id UUID REFERENCES channel_identities(id),
  sender_account_user_id UUID REFERENCES users(id),
  source_message_id TEXT,
  source_reply_to_message_id TEXT,
  role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'system', 'tool')),
  content JSONB NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  usage JSONB,
  session_mode TEXT NOT NULL DEFAULT 'chat' CHECK (session_mode IN ('chat', 'discuss', 'heartbeat', 'schedule', 'subagent')),
  runtime_type TEXT NOT NULL DEFAULT 'model' CHECK (runtime_type IN ('model', 'acp_agent')),
  model_id UUID REFERENCES models(id) ON DELETE SET NULL,
  compact_id UUID,
  event_id UUID REFERENCES bot_session_events(id) ON DELETE SET NULL,
  display_text TEXT,
  turn_id UUID,
  turn_position BIGINT,
  turn_message_seq BIGINT,
  turn_visible BOOLEAN NOT NULL DEFAULT false,
  turn_superseded_by_turn_id UUID,
  turn_superseded_at TIMESTAMPTZ,
  turn_superseded_reason TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_bot_history_messages_bot_created ON bot_history_messages(bot_id, created_at);
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_compact ON bot_history_messages(compact_id);
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_session
  ON bot_history_messages(session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_session_role_created
  ON bot_history_messages(session_id, role, created_at, id);
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_turn
  ON bot_history_messages(turn_id, turn_message_seq, created_at, id)
  WHERE turn_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_history_messages_turn_seq_unique
  ON bot_history_messages(turn_id, turn_message_seq)
  WHERE turn_id IS NOT NULL AND turn_message_seq IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_visible_session_order
  ON bot_history_messages(session_id, turn_position DESC, turn_message_seq DESC, created_at DESC, id DESC)
  WHERE turn_visible = true
    AND turn_id IS NOT NULL
    AND turn_position IS NOT NULL
    AND turn_message_seq IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_visible_session_source_order
  ON bot_history_messages(session_id, source_message_id, turn_position DESC, turn_message_seq DESC, created_at DESC, id DESC)
  WHERE turn_visible = true
    AND source_message_id IS NOT NULL
    AND turn_id IS NOT NULL
    AND turn_position IS NOT NULL
    AND turn_message_seq IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_session_source
  ON bot_history_messages(session_id, source_message_id);
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_session_reply
  ON bot_history_messages(session_id, source_reply_to_message_id);
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_subagent_fork_context
  ON bot_history_messages(session_id, turn_position, turn_message_seq)
  WHERE turn_visible = false
    AND session_mode = 'subagent'
    AND metadata->>'context_scope' = 'subagent_fork';

CREATE OR REPLACE VIEW bot_visible_history_messages AS
SELECT
  m.turn_id,
  m.turn_position,
  m.turn_message_seq,
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id,
  m.source_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.compact_id,
  m.session_mode,
  m.runtime_type,
  m.model_id,
  m.event_id,
  m.display_text,
  m.created_at
FROM bot_history_messages m
WHERE m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL;

CREATE TABLE IF NOT EXISTS bot_session_discuss_cursors (
  session_id UUID NOT NULL REFERENCES bot_sessions(id) ON DELETE CASCADE,
  scope_key TEXT NOT NULL DEFAULT 'default',
  route_id UUID REFERENCES bot_channel_routes(id) ON DELETE SET NULL,
  source TEXT NOT NULL DEFAULT '',
  consumed_cursor BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (session_id, scope_key)
);

CREATE INDEX IF NOT EXISTS idx_bot_session_discuss_cursors_route
  ON bot_session_discuss_cursors(route_id)
  WHERE route_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS tool_approval_requests (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  session_id UUID NOT NULL REFERENCES bot_sessions(id) ON DELETE CASCADE,
  route_id UUID REFERENCES bot_channel_routes(id) ON DELETE SET NULL,
  channel_identity_id UUID REFERENCES channel_identities(id) ON DELETE SET NULL,
  workspace_target_id TEXT NOT NULL DEFAULT '',
  tool_call_id TEXT NOT NULL,
  tool_name TEXT NOT NULL,
  operation TEXT NOT NULL,
  tool_input JSONB NOT NULL,
  short_id INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  runtime_fencing_token BIGINT,
  decision_reason TEXT NOT NULL DEFAULT '',
  requested_by_channel_identity_id UUID REFERENCES channel_identities(id) ON DELETE SET NULL,
  decided_by_channel_identity_id UUID REFERENCES channel_identities(id) ON DELETE SET NULL,
  requested_message_id UUID REFERENCES bot_history_messages(id) ON DELETE SET NULL,
  prompt_message_id UUID REFERENCES bot_history_messages(id) ON DELETE SET NULL,
  prompt_external_message_id TEXT NOT NULL DEFAULT '',
  source_platform TEXT NOT NULL DEFAULT '',
  reply_target TEXT NOT NULL DEFAULT '',
  conversation_type TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_at TIMESTAMPTZ,
  CONSTRAINT tool_approval_operation_check CHECK (operation IN ('read', 'write', 'exec')),
  CONSTRAINT tool_approval_status_check CHECK (status IN ('pending', 'approved', 'rejected', 'expired', 'cancelled')),
  CONSTRAINT tool_approval_short_id_unique UNIQUE (session_id, short_id),
  CONSTRAINT tool_approval_tool_call_unique UNIQUE (session_id, tool_call_id)
);

CREATE INDEX IF NOT EXISTS idx_tool_approval_bot_status_created
  ON tool_approval_requests(bot_id, status, created_at);
CREATE INDEX IF NOT EXISTS idx_tool_approval_session_status_created
  ON tool_approval_requests(session_id, status, created_at);
CREATE INDEX IF NOT EXISTS idx_tool_approval_prompt_external
  ON tool_approval_requests(prompt_external_message_id)
  WHERE prompt_external_message_id != '';

CREATE TABLE IF NOT EXISTS user_input_requests (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  session_id UUID NOT NULL REFERENCES bot_sessions(id) ON DELETE CASCADE,
  route_id UUID REFERENCES bot_channel_routes(id) ON DELETE SET NULL,
  channel_identity_id UUID REFERENCES channel_identities(id) ON DELETE SET NULL,
  workspace_target_id TEXT NOT NULL DEFAULT '',
  tool_call_id TEXT NOT NULL,
  tool_name TEXT NOT NULL DEFAULT 'ask_user',
  short_id INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  runtime_fencing_token BIGINT,
  input_json JSONB NOT NULL,
  ui_payload_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  interaction_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  interaction_revision INTEGER NOT NULL DEFAULT 0,
  result_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  provider_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  requested_by_channel_identity_id UUID REFERENCES channel_identities(id) ON DELETE SET NULL,
  responded_by_channel_identity_id UUID REFERENCES channel_identities(id) ON DELETE SET NULL,
  assistant_message_id UUID REFERENCES bot_history_messages(id) ON DELETE SET NULL,
  tool_result_message_id UUID REFERENCES bot_history_messages(id) ON DELETE SET NULL,
  prompt_message_id UUID REFERENCES bot_history_messages(id) ON DELETE SET NULL,
  prompt_external_message_id TEXT NOT NULL DEFAULT '',
  source_platform TEXT NOT NULL DEFAULT '',
  reply_target TEXT NOT NULL DEFAULT '',
  conversation_type TEXT NOT NULL DEFAULT '',
  expires_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  responded_at TIMESTAMPTZ,
  canceled_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT user_input_tool_name_check CHECK (tool_name = 'ask_user'),
  CONSTRAINT user_input_status_check CHECK (status IN ('pending', 'submitted', 'canceled', 'expired', 'failed')),
  CONSTRAINT user_input_short_id_unique UNIQUE (session_id, short_id),
  CONSTRAINT user_input_tool_call_unique UNIQUE (session_id, tool_call_id)
);

CREATE INDEX IF NOT EXISTS idx_user_input_bot_status_created
  ON user_input_requests(bot_id, status, created_at);
CREATE INDEX IF NOT EXISTS idx_user_input_session_status_created
  ON user_input_requests(session_id, status, created_at);
CREATE INDEX IF NOT EXISTS idx_user_input_prompt_external
  ON user_input_requests(prompt_external_message_id)
  WHERE prompt_external_message_id != '';

CREATE TABLE IF NOT EXISTS containers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  container_id TEXT NOT NULL,
  container_name TEXT NOT NULL,
  image TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'created',
  namespace TEXT NOT NULL DEFAULT 'default',
  auto_start BOOLEAN NOT NULL DEFAULT true,
  container_path TEXT NOT NULL DEFAULT '/data',
  workspace_backend TEXT NOT NULL DEFAULT 'container',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_started_at TIMESTAMPTZ,
  last_stopped_at TIMESTAMPTZ,
  CONSTRAINT containers_container_id_unique UNIQUE (container_id),
  CONSTRAINT containers_container_name_unique UNIQUE (container_name)
);

CREATE INDEX IF NOT EXISTS idx_containers_bot_id ON containers(bot_id);

-- bot_workspace_resource_limits: desired per-bot workspace resource limits.
-- A value of 0 means unlimited for that resource.
CREATE TABLE IF NOT EXISTS bot_workspace_resource_limits (
  bot_id UUID PRIMARY KEY REFERENCES bots(id) ON DELETE CASCADE,
  cpu_millicores BIGINT NOT NULL DEFAULT 0,
  memory_bytes BIGINT NOT NULL DEFAULT 0,
  storage_bytes BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT bot_workspace_resource_limits_cpu_check CHECK (cpu_millicores >= 0),
  CONSTRAINT bot_workspace_resource_limits_memory_check CHECK (memory_bytes >= 0),
  CONSTRAINT bot_workspace_resource_limits_storage_check CHECK (storage_bytes >= 0)
);

CREATE TABLE IF NOT EXISTS snapshots (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  container_id TEXT NOT NULL REFERENCES containers(container_id) ON DELETE CASCADE,
  runtime_snapshot_name TEXT NOT NULL,
  display_name TEXT,
  parent_runtime_snapshot_name TEXT,
  snapshotter TEXT NOT NULL,
  source TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_snapshots_container_runtime_name
  ON snapshots(container_id, runtime_snapshot_name);
CREATE INDEX IF NOT EXISTS idx_snapshots_container_created_at
  ON snapshots(container_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_snapshots_runtime_name
  ON snapshots(runtime_snapshot_name);

CREATE TABLE IF NOT EXISTS container_versions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  container_id TEXT NOT NULL REFERENCES containers(container_id) ON DELETE CASCADE,
  snapshot_id UUID NOT NULL REFERENCES snapshots(id) ON DELETE RESTRICT,
  version INTEGER NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (container_id, version)
);

CREATE INDEX IF NOT EXISTS idx_container_versions_container_id ON container_versions(container_id);
CREATE INDEX IF NOT EXISTS idx_container_versions_snapshot_id ON container_versions(snapshot_id);

CREATE TABLE IF NOT EXISTS lifecycle_events (
  id TEXT PRIMARY KEY,
  container_id TEXT NOT NULL REFERENCES containers(container_id) ON DELETE CASCADE,
  event_type TEXT NOT NULL,
  payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_lifecycle_events_container_id ON lifecycle_events(container_id);
CREATE INDEX IF NOT EXISTS idx_lifecycle_events_event_type ON lifecycle_events(event_type);

CREATE TABLE IF NOT EXISTS schedule (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  description TEXT NOT NULL,
  pattern TEXT NOT NULL,
  max_calls INTEGER,
  current_calls INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  enabled BOOLEAN NOT NULL DEFAULT true,
  command TEXT NOT NULL,
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_schedule_bot_id ON schedule(bot_id);
CREATE INDEX IF NOT EXISTS idx_schedule_enabled ON schedule(enabled);

-- storage_providers: pluggable object storage backends
CREATE TABLE IF NOT EXISTS storage_providers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  provider TEXT NOT NULL,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT storage_providers_name_unique UNIQUE (name),
  CONSTRAINT storage_providers_provider_check CHECK (provider IN ('localfs', 's3', 'gcs'))
);

-- bot_storage_bindings: per-bot storage backend selection
CREATE TABLE IF NOT EXISTS bot_storage_bindings (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  storage_provider_id UUID NOT NULL REFERENCES storage_providers(id) ON DELETE CASCADE,
  base_path TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT bot_storage_bindings_unique UNIQUE (bot_id)
);

CREATE INDEX IF NOT EXISTS idx_bot_storage_bindings_bot_id ON bot_storage_bindings(bot_id);

-- bot_history_message_assets: soft link (message -> content_hash only).
-- MIME, size, storage_key are derived from storage at read time.
CREATE TABLE IF NOT EXISTS bot_history_message_assets (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  message_id UUID NOT NULL REFERENCES bot_history_messages(id) ON DELETE CASCADE,
  role TEXT NOT NULL DEFAULT 'attachment',
  ordinal INTEGER NOT NULL DEFAULT 0,
  content_hash TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT message_asset_content_unique UNIQUE (message_id, content_hash)
);

CREATE INDEX IF NOT EXISTS idx_message_assets_message_id ON bot_history_message_assets(message_id);


-- bot_heartbeat_logs: structured execution records for periodic heartbeat checks.
CREATE TABLE IF NOT EXISTS bot_heartbeat_logs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  session_id UUID REFERENCES bot_sessions(id) ON DELETE SET NULL,
  status TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok', 'alert', 'error')),
  result_text TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  usage JSONB,
  model_id UUID REFERENCES models(id) ON DELETE SET NULL,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_heartbeat_logs_bot_started ON bot_heartbeat_logs(bot_id, started_at DESC);

CREATE TABLE IF NOT EXISTS bot_history_message_compacts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  session_id UUID REFERENCES bot_sessions(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ok', 'error')),
  summary TEXT NOT NULL DEFAULT '',
  message_count INTEGER NOT NULL DEFAULT 0,
  error_message TEXT NOT NULL DEFAULT '',
  usage JSONB,
  model_id UUID REFERENCES models(id) ON DELETE SET NULL,
  artifact_version INTEGER NOT NULL DEFAULT 1,
  coverage JSONB NOT NULL DEFAULT '[]'::jsonb,
  anchor_start_ms BIGINT NOT NULL DEFAULT 0,
  anchor_end_ms BIGINT NOT NULL DEFAULT 0,
  artifact_level INTEGER NOT NULL DEFAULT 0,
  parent_ids UUID[] NOT NULL DEFAULT '{}'::uuid[],
  superseded_by UUID REFERENCES bot_history_message_compacts(id) ON DELETE SET NULL,
  superseded_at TIMESTAMPTZ,
  compaction_epoch BIGINT NOT NULL DEFAULT 0,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_compacts_bot_session ON bot_history_message_compacts(bot_id, session_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_compacts_owner_epoch ON bot_history_message_compacts(bot_id, session_id, compaction_epoch, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_compacts_active_session ON bot_history_message_compacts(session_id, anchor_start_ms, started_at) WHERE status = 'ok' AND superseded_at IS NULL;

ALTER TABLE bot_history_messages ADD CONSTRAINT fk_compact_id FOREIGN KEY (compact_id) REFERENCES bot_history_message_compacts(id) ON DELETE SET NULL;

-- schedule_logs: structured execution records for scheduled tasks.
CREATE TABLE IF NOT EXISTS schedule_logs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  schedule_id UUID NOT NULL REFERENCES schedule(id) ON DELETE CASCADE,
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  session_id UUID REFERENCES bot_sessions(id) ON DELETE SET NULL,
  status TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok', 'error')),
  result_text TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  usage JSONB,
  model_id UUID REFERENCES models(id) ON DELETE SET NULL,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_schedule_logs_schedule ON schedule_logs(schedule_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_schedule_logs_bot ON schedule_logs(bot_id, started_at DESC);

-- email_providers: pluggable email service backends
CREATE TABLE IF NOT EXISTS email_providers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  provider TEXT NOT NULL,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT email_providers_user_name_unique UNIQUE (user_id, name)
);

CREATE INDEX IF NOT EXISTS idx_email_providers_user_id ON email_providers(user_id);
CREATE INDEX IF NOT EXISTS idx_providers_provider_template_id ON providers(provider_template_id);

-- email_oauth_tokens: stored OAuth2 tokens for Gmail email providers
CREATE TABLE IF NOT EXISTS email_oauth_tokens (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email_provider_id UUID NOT NULL UNIQUE REFERENCES email_providers(id) ON DELETE CASCADE,
  email_address TEXT NOT NULL DEFAULT '',
  access_token TEXT NOT NULL DEFAULT '',
  refresh_token TEXT NOT NULL DEFAULT '',
  expires_at TIMESTAMPTZ,
  scope TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_email_oauth_tokens_state ON email_oauth_tokens(state) WHERE state != '';

-- bot_email_bindings: per-bot email provider binding with read/write/delete permissions
CREATE TABLE IF NOT EXISTS bot_email_bindings (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  email_provider_id UUID NOT NULL REFERENCES email_providers(id) ON DELETE CASCADE,
  email_address TEXT NOT NULL,
  can_read BOOLEAN NOT NULL DEFAULT TRUE,
  can_write BOOLEAN NOT NULL DEFAULT TRUE,
  can_delete BOOLEAN NOT NULL DEFAULT FALSE,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT bot_email_bindings_unique UNIQUE (bot_id, email_provider_id)
);

CREATE INDEX IF NOT EXISTS idx_bot_email_bindings_bot_id ON bot_email_bindings(bot_id);
CREATE INDEX IF NOT EXISTS idx_bot_email_bindings_provider_id ON bot_email_bindings(email_provider_id);

-- email_outbox: outbound email audit log
CREATE TABLE IF NOT EXISTS email_outbox (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_id UUID NOT NULL REFERENCES email_providers(id) ON DELETE CASCADE,
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  message_id TEXT NOT NULL DEFAULT '',
  from_address TEXT NOT NULL DEFAULT '',
  to_addresses JSONB NOT NULL DEFAULT '[]'::jsonb,
  subject TEXT NOT NULL DEFAULT '',
  body_text TEXT NOT NULL DEFAULT '',
  body_html TEXT NOT NULL DEFAULT '',
  attachments JSONB NOT NULL DEFAULT '[]'::jsonb,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed')),
  error TEXT NOT NULL DEFAULT '',
  sent_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_email_outbox_provider_id ON email_outbox(provider_id);
CREATE INDEX IF NOT EXISTS idx_email_outbox_bot_id ON email_outbox(bot_id, created_at DESC);

-- provider_oauth_tokens: OAuth2 tokens for LLM providers (e.g. OpenAI Codex OAuth)
CREATE TABLE IF NOT EXISTS provider_oauth_tokens (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_id UUID NOT NULL UNIQUE REFERENCES providers(id) ON DELETE CASCADE,
  access_token TEXT NOT NULL DEFAULT '',
  refresh_token TEXT NOT NULL DEFAULT '',
  expires_at TIMESTAMPTZ,
  scope TEXT NOT NULL DEFAULT '',
  token_type TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT '',
  pkce_code_verifier TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_provider_oauth_tokens_state ON provider_oauth_tokens(state) WHERE state != '';

-- user_provider_oauth_tokens: legacy per-user OAuth2 storage retained for rollback compatibility.
-- Active Codex and GitHub Copilot credentials are provider-scoped in provider_oauth_tokens.
CREATE TABLE IF NOT EXISTS user_provider_oauth_tokens (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_id UUID NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  access_token TEXT NOT NULL DEFAULT '',
  refresh_token TEXT NOT NULL DEFAULT '',
  expires_at TIMESTAMPTZ,
  scope TEXT NOT NULL DEFAULT '',
  token_type TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT '',
  pkce_code_verifier TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT user_provider_oauth_tokens_provider_user_unique UNIQUE (provider_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_user_provider_oauth_tokens_state ON user_provider_oauth_tokens(state) WHERE state != '';

-- bot_user_grants: workspace user access grants for a bot.
-- subject_type 'user' targets a specific workspace member; 'everyone' targets all members.
-- permissions is a JSON string array of grant scopes ('chat', 'manage').
CREATE TABLE IF NOT EXISTS bot_user_grants (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  subject_type TEXT NOT NULL,
  user_id UUID REFERENCES users(id) ON DELETE CASCADE,
  permissions JSONB NOT NULL DEFAULT '[]'::jsonb,
  created_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT bot_user_grants_subject_type_check CHECK (subject_type IN ('user', 'everyone')),
  CONSTRAINT bot_user_grants_subject_value_check CHECK (
    (subject_type = 'user' AND user_id IS NOT NULL) OR
    (subject_type = 'everyone' AND user_id IS NULL)
  )
);

CREATE INDEX IF NOT EXISTS idx_bot_user_grants_bot_id ON bot_user_grants(bot_id);
CREATE INDEX IF NOT EXISTS idx_bot_user_grants_user_id ON bot_user_grants(user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_user_grants_unique_user ON bot_user_grants(bot_id, user_id) WHERE subject_type = 'user';
CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_user_grants_unique_everyone ON bot_user_grants(bot_id) WHERE subject_type = 'everyone';

-- Memory wiki/graph (canonical memory content source of truth).
CREATE TABLE IF NOT EXISTS memory_nodes (
    id               TEXT        PRIMARY KEY,
    bot_id           UUID        NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    body             TEXT        NOT NULL,
    hash             TEXT        NOT NULL,
    layer            TEXT        NOT NULL DEFAULT 'note',
    fact_type        TEXT        NOT NULL DEFAULT '',
    subject          TEXT        NOT NULL DEFAULT '',
    confidence       REAL        NOT NULL DEFAULT 0.5,
    metadata         JSONB       NOT NULL DEFAULT '{}'::jsonb,
    source_message_ids JSONB     NOT NULL DEFAULT '[]'::jsonb,
    profile_ref      TEXT        NOT NULL DEFAULT '',
    topic            TEXT        NOT NULL DEFAULT '',
    captured_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at       TIMESTAMPTZ,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT memory_nodes_confidence_check CHECK (confidence >= 0 AND confidence <= 1)
);

CREATE INDEX IF NOT EXISTS idx_memory_nodes_bot_layer  ON memory_nodes (bot_id, layer);
CREATE INDEX IF NOT EXISTS idx_memory_nodes_bot_topic  ON memory_nodes (bot_id, topic);
CREATE INDEX IF NOT EXISTS idx_memory_nodes_bot_prof   ON memory_nodes (bot_id, profile_ref);
CREATE INDEX IF NOT EXISTS idx_memory_nodes_updated    ON memory_nodes (bot_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS memory_edges (
    id          BIGSERIAL    PRIMARY KEY,
    bot_id      UUID         NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    src_node    TEXT         NOT NULL,
    dst_node    TEXT         NOT NULL,
    rel         TEXT         NOT NULL,
    weight      REAL         NOT NULL DEFAULT 1.0,
    metadata    JSONB        NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT memory_edges_unique UNIQUE (bot_id, src_node, dst_node, rel)
);

CREATE INDEX IF NOT EXISTS idx_memory_edges_src  ON memory_edges (bot_id, src_node);
CREATE INDEX IF NOT EXISTS idx_memory_edges_dst  ON memory_edges (bot_id, dst_node);
CREATE INDEX IF NOT EXISTS idx_memory_edges_rel  ON memory_edges (bot_id, rel);


-- ---------------------------------------------------------------------------
-- Canonical team and membership schema
-- ---------------------------------------------------------------------------
-- 0001 must describe the final PostgreSQL schema. The incremental 0112 and
-- 0115 migrations retain the data-preserving upgrade path for existing
-- installations; the same replay-safe finalizers are applied here so a
-- baseline-only database has the identical Team and membership boundary.

-- ---------------------------------------------------------------------------
-- Team root
-- ---------------------------------------------------------------------------
-- Introduce the teams root table and seed the default singleton team.
--
-- This is the first migration of the team-core work. teams is the unique
-- root special case: its own id IS the team id, so it must NOT carry a
-- redundant team_id column. Existing installs upgrade in place (no wipe): the
-- single default team is seeded idempotently and every existing business row
-- is later backfilled to DefaultTeamID by subsequent migrations.
--
-- DefaultTeamID is the fixed constant published in internal/team/id.go
-- (00000000-0000-0000-0000-000000000001). It must never be generated per install.

CREATE TABLE IF NOT EXISTS teams (
    id         UUID        PRIMARY KEY,
    slug       TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata   JSONB       NOT NULL DEFAULT '{}'::jsonb
);

-- slug is an optional directory field, not an identity/authorization boundary.
-- When present it must be unique cell-wide; NULL slugs are allowed and excluded.
CREATE UNIQUE INDEX IF NOT EXISTS teams_slug_unique ON teams (slug) WHERE slug IS NOT NULL;

-- Seed the singleton team idempotently. Existing self-hosted installations
-- continue to use this team without any configuration changes.
DO $seed_default_team$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM teams
         WHERE id = '00000000-0000-0000-0000-000000000001'
    ) THEN
        INSERT INTO teams (id, slug)
        VALUES ('00000000-0000-0000-0000-000000000001', 'default');
    END IF;
END
$seed_default_team$;

-- ---------------------------------------------------------------------------
-- Team context
-- ---------------------------------------------------------------------------
-- Add the fail-closed team context helper used by team-scoped queries and
-- row-level security policies.

CREATE OR REPLACE FUNCTION public.memoh_current_team_id()
RETURNS uuid
LANGUAGE plpgsql
STABLE
SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
  raw text;
BEGIN
  raw := pg_catalog.current_setting('memoh.team_id', true);
  IF raw IS NULL OR pg_catalog.btrim(raw) = '' THEN
    RAISE EXCEPTION 'memoh.team_id is not set'
      USING ERRCODE = '42501';
  END IF;
  BEGIN
    RETURN raw::uuid;
  EXCEPTION
    WHEN invalid_text_representation THEN
      RAISE EXCEPTION 'memoh.team_id is invalid'
        USING ERRCODE = '22P02';
  END;
END;
$$;

-- Create the final membership relation before applying the shared team-core
-- finalizer. Its presence tells the replay-safe 0112 logic that users are
-- already global principals rather than legacy team-owned accounts.
CREATE TABLE IF NOT EXISTS public.team_members (
    team_id    UUID        NOT NULL DEFAULT public.memoh_current_team_id(),
    user_id    UUID        NOT NULL,
    role       user_role   NOT NULL DEFAULT 'member',
    is_active  BOOLEAN     NOT NULL DEFAULT true,
    data_root  TEXT,
    title_model_id UUID,
    metadata   JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, user_id),
    CONSTRAINT team_members_team_id_fkey
        FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT,
    CONSTRAINT team_members_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS team_members_user_id_idx
    ON public.team_members (user_id);

-- ---------------------------------------------------------------------------
-- Team columns and singleton backfill
-- ---------------------------------------------------------------------------
-- Add a nullable team_id column to every team business table (STATIC ALTERs
-- so sqlc can see the column) and backfill it to the default singleton team
-- before team-scoped constraints are installed.
--
-- New incremental (existing migrations untouched). team_id is added NULLABLE,
-- given a DEFAULT of public.memoh_current_team_id() (so INSERTs auto-fill the current
-- team), and backfilled to the default singleton; a later migration tightens
-- it to NOT NULL after composite keys/FKs land. Existing installs upgrade in
-- place (no wipe).
--
-- The ADD COLUMN statements are STATIC (one literal ALTER per table) rather than
-- a dynamic DO/EXECUTE loop, because sqlc parses the schema declaratively and
-- cannot see columns added inside DO/EXECUTE blocks. `ALTER TABLE IF EXISTS ...
-- ADD COLUMN IF NOT EXISTS` is a safe no-op on the legacy upgrade path where a
-- table (e.g. tts_providers/tts_models) does not exist. The table list is the
-- applied fresh-install team set (53 tables), excluding schema_migrations
-- tooling and the teams root.

ALTER TABLE IF EXISTS public.bot_acl_rules ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bot_channel_admins ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bot_channel_configs ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bot_channel_routes ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bot_email_bindings ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bot_heartbeat_logs ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bot_history_message_assets ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bot_history_message_compacts ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bot_history_messages ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bot_plugin_installations ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bot_plugin_resources ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bot_remote_runtime_bindings ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bot_session_discuss_cursors ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bot_session_events ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bot_sessions ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bot_storage_bindings ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bot_user_grants ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bot_workspace_resource_limits ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.bots ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.channel_identities ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.channel_link_codes ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.container_versions ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.containers ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.email_oauth_tokens ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.email_outbox ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.email_providers ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.fetch_providers ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.lifecycle_events ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.mcp_connections ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.mcp_oauth_tokens ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.media_assets ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.memory_edges ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.memory_nodes ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.memory_providers ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.model_variants ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.models ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.provider_oauth_tokens ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.providers ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.schedule ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.schedule_logs ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.search_providers ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.snapshots ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.storage_providers ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.tasks ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.tool_approval_requests ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.tts_models ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.tts_providers ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.user_channel_bindings ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.user_channel_identity_bindings ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.user_input_requests ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.user_provider_oauth_tokens ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE IF EXISTS public.user_runtimes ADD COLUMN IF NOT EXISTS team_id uuid;
-- A canonical 0001 already has global users plus team_members. Legacy
-- databases do not, so only the legacy path needs the transitional users
-- team_id column that 0115 later moves into team_members.
DO $users_legacy_team_column$
BEGIN
    IF to_regclass('public.team_members') IS NULL THEN
        ALTER TABLE IF EXISTS public.users ADD COLUMN IF NOT EXISTS team_id uuid;
    END IF;
END
$users_legacy_team_column$;


-- Give team_id a DEFAULT so any INSERT (sqlc-generated or raw) auto-fills the
-- current team from the session/transaction GUC. Fail-closed: if the GUC is
-- unset, public.memoh_current_team_id() raises rather than inserting a NULL/guessed team.
ALTER TABLE IF EXISTS public.bot_acl_rules ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bot_channel_admins ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bot_channel_configs ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bot_channel_routes ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bot_email_bindings ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bot_heartbeat_logs ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bot_history_message_assets ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bot_history_message_compacts ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bot_history_messages ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bot_plugin_installations ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bot_plugin_resources ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bot_remote_runtime_bindings ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bot_session_discuss_cursors ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bot_session_events ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bot_sessions ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bot_storage_bindings ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bot_user_grants ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bot_workspace_resource_limits ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.bots ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.channel_identities ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.channel_link_codes ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.container_versions ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.containers ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.email_oauth_tokens ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.email_outbox ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.email_providers ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.fetch_providers ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.lifecycle_events ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.mcp_connections ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.mcp_oauth_tokens ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.media_assets ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.memory_edges ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.memory_nodes ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.memory_providers ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.model_variants ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.models ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.provider_oauth_tokens ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.providers ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.schedule ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.schedule_logs ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.search_providers ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.snapshots ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.storage_providers ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.tasks ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.tool_approval_requests ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.tts_models ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.tts_providers ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.user_channel_bindings ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.user_channel_identity_bindings ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.user_input_requests ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.user_provider_oauth_tokens ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
ALTER TABLE IF EXISTS public.user_runtimes ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
DO $users_legacy_team_default$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM pg_catalog.pg_attribute
         WHERE attrelid = 'public.users'::regclass
           AND attname = 'team_id'
           AND NOT attisdropped
    ) THEN
        ALTER TABLE public.users
            ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id();
    END IF;
END
$users_legacy_team_default$;

-- Backfill every present team table to the default singleton. Dynamic because
-- sqlc ignores UPDATE statements; enumerating the applied schema keeps this
-- correct across the fresh/legacy path divergence.
DO $$
DECLARE
    tbl text;
    default_team constant uuid := '00000000-0000-0000-0000-000000000001';
BEGIN
    FOR tbl IN
        SELECT c.relname
          FROM pg_catalog.pg_class c
          JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = 'public'
           AND c.relkind IN ('r', 'p')
           AND EXISTS (
               SELECT 1
                 FROM pg_attribute a
                 JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
                WHERE a.attrelid = c.oid
                  AND a.attname = 'team_id'
                  AND NOT a.attisdropped
                  AND pg_get_expr(d.adbin, d.adrelid) LIKE '%memoh_current_team_id()%'
           )
         ORDER BY c.relname
    LOOP
        EXECUTE format('UPDATE public.%I SET team_id = %L WHERE team_id IS NULL', tbl, default_team);
    END LOOP;
END
$$;

-- ---------------------------------------------------------------------------
-- Team-scoped keys and foreign keys
-- ---------------------------------------------------------------------------
-- Add team-scoped unique keys and composite foreign keys while preserving the
-- existing primary keys and delete behavior.
--
-- This work is atomic because in PostgreSQL an FK binds to a specific unique
-- index: you cannot rebuild a referenced key without dropping and recreating its
-- dependent FKs in the same operation. Splitting across migrations would fight
-- that coupling; doing it atomically is both correct and simpler to reason about.
--
-- Preconditions (from earlier sections): every team table already has a
-- backfilled team_id and the teams root exists. Team tables are identified
-- by the team default installed above, so user-managed
-- tables in the public schema are not modified.
--
-- Existing ON DELETE SET NULL constraints use PostgreSQL's column-list form,
-- SET NULL (child_column), so the reference is cleared without clearing the
-- non-null team_id column.

DO $$
DECLARE
    rec record;
    cols text;
    delete_action text;
    update_action text;
BEGIN
    CREATE TEMP TABLE _team_tables (table_name text PRIMARY KEY) ON COMMIT DROP;
    INSERT INTO _team_tables
    SELECT c.relname
      FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace
      JOIN pg_attribute a ON a.attrelid = c.oid
      JOIN pg_attrdef d ON d.adrelid = c.oid AND d.adnum = a.attnum
     WHERE n.nspname = 'public'
       AND c.relkind IN ('r', 'p')
       AND c.relname <> 'team_members'
       AND a.attname = 'team_id'
       AND NOT a.attisdropped
       AND pg_get_expr(d.adbin, d.adrelid) LIKE '%memoh_current_team_id()%';

    CREATE TEMP TABLE _fk_saved ON COMMIT DROP AS
    SELECT con.oid,
           c.relname            AS child_table,
           con.conname          AS fk_name,
           rt.relname           AS parent_table,
           con.confdeltype      AS del_type,
           con.confupdtype      AS upd_type,
           (SELECT a.attname FROM pg_attribute a
             WHERE a.attrelid = con.conrelid AND a.attnum = con.conkey[1])  AS child_col,
           (SELECT a.attname FROM pg_attribute a
             WHERE a.attrelid = con.confrelid AND a.attnum = con.confkey[1]) AS parent_col,
           cardinality(con.conkey) AS ncols
      FROM pg_constraint con
      JOIN pg_class c  ON c.oid  = con.conrelid
      JOIN pg_class rt ON rt.oid = con.confrelid
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE con.contype = 'f'
       AND n.nspname = 'public'
       AND c.relname IN (SELECT table_name FROM _team_tables)
       AND rt.relname IN (SELECT table_name FROM _team_tables)
       AND cardinality(con.conkey) = 1;

    -- Safety: this algorithm only handles single-column business FKs.
    IF EXISTS (SELECT 1 FROM _fk_saved WHERE ncols <> 1) THEN
        RAISE EXCEPTION 'multi-column FK present; composite re-key algorithm needs revision';
    END IF;

    IF EXISTS (SELECT 1 FROM _fk_saved WHERE upd_type IN ('n', 'd')) THEN
        RAISE EXCEPTION 'ON UPDATE SET NULL/DEFAULT requires an explicit team-safe migration';
    END IF;

    FOR rec IN SELECT child_table, fk_name FROM _fk_saved LOOP
        EXECUTE format('ALTER TABLE public.%I DROP CONSTRAINT %I', rec.child_table, rec.fk_name);
    END LOOP;

    -- Keep existing primary keys stable. Add a team-prefixed unique key for
    -- each primary key so composite foreign keys have a valid target.
    FOR rec IN
        SELECT c.relname AS table_name, con.conname, con.conrelid, con.conkey
          FROM pg_constraint con
          JOIN pg_class c ON c.oid = con.conrelid
          JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE con.contype = 'p' AND n.nspname = 'public'
           AND c.relname IN (SELECT table_name FROM _team_tables)
    LOOP
        SELECT 'team_id, ' || string_agg(quote_ident(a.attname), ', ' ORDER BY k.ord)
          INTO cols
          FROM unnest(rec.conkey) WITH ORDINALITY AS k(attnum, ord)
          JOIN pg_attribute a ON a.attrelid = rec.conrelid AND a.attnum = k.attnum;
        EXECUTE format('ALTER TABLE public.%I ALTER COLUMN team_id SET NOT NULL', rec.table_name);
        IF NOT EXISTS (
            SELECT 1 FROM pg_constraint
             WHERE conrelid = rec.conrelid
               AND conname = 'memoh_team_key_' || substr(md5(rec.table_name || ':' || rec.conname), 1, 12)
        ) THEN
            EXECUTE format('ALTER TABLE public.%I ADD CONSTRAINT %I UNIQUE (%s)',
                rec.table_name,
                'memoh_team_key_' || substr(md5(rec.table_name || ':' || rec.conname), 1, 12),
                cols);
        END IF;
    END LOOP;

    -- ===== Phase 3: rebuild UNIQUE constraints with team_id prepended =====
    FOR rec IN
        SELECT c.relname AS table_name, con.conname, con.conrelid, con.conkey,
               i.indnullsnotdistinct AS nulls_not_distinct
          FROM pg_constraint con
          JOIN pg_class c ON c.oid = con.conrelid
          JOIN pg_namespace n ON n.oid = c.relnamespace
          JOIN pg_index i ON i.indexrelid = con.conindid
         WHERE con.contype = 'u' AND n.nspname = 'public'
           AND c.relname IN (SELECT table_name FROM _team_tables)
           AND con.conname NOT LIKE 'memoh_team_key_%'
    LOOP
        IF (SELECT attname FROM pg_attribute
              WHERE attrelid = rec.conrelid AND attnum = rec.conkey[1]) = 'team_id' THEN
            CONTINUE;
        END IF;
        SELECT 'team_id, ' || string_agg(quote_ident(a.attname), ', ' ORDER BY k.ord)
          INTO cols
          FROM unnest(rec.conkey) WITH ORDINALITY AS k(attnum, ord)
          JOIN pg_attribute a ON a.attrelid = rec.conrelid AND a.attnum = k.attnum;
        EXECUTE format('ALTER TABLE public.%I DROP CONSTRAINT %I', rec.table_name, rec.conname);
        -- Preserve NULLS NOT DISTINCT: a bare UNIQUE would widen the semantics
        -- (NULLs become distinct), letting duplicate rows with NULL key columns
        -- through (e.g. bot_acl_rules_unique_target).
        EXECUTE format('ALTER TABLE public.%I ADD CONSTRAINT %I UNIQUE %s (%s)',
                       rec.table_name, rec.conname,
                       CASE WHEN rec.nulls_not_distinct THEN 'NULLS NOT DISTINCT' ELSE '' END,
                       cols);
    END LOOP;

    FOR rec IN SELECT * FROM _fk_saved LOOP
        update_action := CASE rec.upd_type WHEN 'c' THEN 'CASCADE' WHEN 'r' THEN 'RESTRICT'
                          ELSE 'NO ACTION' END;
        delete_action := CASE rec.del_type
            WHEN 'c' THEN 'CASCADE'
            WHEN 'r' THEN 'RESTRICT'
            WHEN 'n' THEN format('SET NULL (%I)', rec.child_col)
            WHEN 'd' THEN format('SET DEFAULT (%I)', rec.child_col)
            ELSE 'NO ACTION'
        END;
        EXECUTE format(
            'ALTER TABLE public.%I ADD CONSTRAINT %I FOREIGN KEY (team_id, %I) '
            || 'REFERENCES public.%I (team_id, %I) ON UPDATE %s ON DELETE %s',
            rec.child_table, rec.fk_name, rec.child_col,
            rec.parent_table, rec.parent_col,
            update_action, delete_action
        );
    END LOOP;

    -- ===== Phase 4b: add root FK (team_id) -> teams(id) on every table =====
    FOR rec IN
        SELECT c.relname AS table_name
          FROM pg_class c
          JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE c.relkind IN ('r', 'p') AND n.nspname = 'public'
           AND c.relname IN (SELECT table_name FROM _team_tables)
    LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_constraint con
              JOIN pg_class rt ON rt.oid = con.confrelid
             WHERE con.contype = 'f'
               AND con.conrelid = ('public.'||quote_ident(rec.table_name))::regclass
               AND rt.relname = 'teams'
        ) THEN
            EXECUTE format(
                'ALTER TABLE public.%I ADD CONSTRAINT %I FOREIGN KEY (team_id) '
                || 'REFERENCES public.teams (id) ON DELETE RESTRICT',
                rec.table_name, rec.table_name || '_team_id_fkey');
        END IF;
    END LOOP;
END
$$;

-- The title model is a per-member preference in the current team. Models are
-- team-owned, so keep the team id in the foreign key to prevent a profile from
-- referencing a model in another team.
ALTER TABLE public.team_members
    DROP CONSTRAINT IF EXISTS team_members_title_model_id_fkey,
    ADD CONSTRAINT team_members_title_model_id_fkey
        FOREIGN KEY (team_id, title_model_id)
        REFERENCES public.models(team_id, id)
        ON DELETE SET NULL (title_model_id);

-- ===== Phase 3b: partial / expression unique indexes with team_id prepended =====
DROP INDEX IF EXISTS idx_bot_channel_external_identity;
CREATE UNIQUE INDEX idx_bot_channel_external_identity
    ON public.bot_channel_configs (team_id, channel_type, external_identity);

DROP INDEX IF EXISTS idx_bot_channel_routes_unique;
CREATE UNIQUE INDEX idx_bot_channel_routes_unique
    ON public.bot_channel_routes
       (team_id, bot_id, channel_type, external_conversation_id, COALESCE(external_thread_id, ''::text));

DROP INDEX IF EXISTS idx_bot_history_messages_turn_seq_unique;
CREATE UNIQUE INDEX idx_bot_history_messages_turn_seq_unique
    ON public.bot_history_messages (team_id, turn_id, turn_message_seq)
    WHERE ((turn_id IS NOT NULL) AND (turn_message_seq IS NOT NULL));

DROP INDEX IF EXISTS idx_bot_user_grants_unique_everyone;
CREATE UNIQUE INDEX idx_bot_user_grants_unique_everyone
    ON public.bot_user_grants (team_id, bot_id)
    WHERE (subject_type = 'everyone'::text);

DROP INDEX IF EXISTS idx_bot_user_grants_unique_user;
CREATE UNIQUE INDEX idx_bot_user_grants_unique_user
    ON public.bot_user_grants (team_id, bot_id, user_id)
    WHERE (subject_type = 'user'::text);

DROP INDEX IF EXISTS idx_bots_name;
CREATE UNIQUE INDEX idx_bots_name
    ON public.bots (team_id, name);

DROP INDEX IF EXISTS idx_session_events_dedup;
CREATE UNIQUE INDEX idx_session_events_dedup
    ON public.bot_session_events (team_id, session_id, event_kind, external_message_id)
    WHERE ((external_message_id IS NOT NULL) AND (external_message_id <> ''::text));

DROP INDEX IF EXISTS idx_snapshots_container_runtime_name;
CREATE UNIQUE INDEX idx_snapshots_container_runtime_name
    ON public.snapshots (team_id, container_id, runtime_snapshot_name);

-- ---------------------------------------------------------------------------
-- Team row-level security
-- ---------------------------------------------------------------------------
-- Enable and force row-level security on Memoh team tables. Policies apply
-- to the connecting role, so installations do not need cluster-wide roles or
-- elevated CREATEROLE/BYPASSRLS privileges.

DO $$
DECLARE
    tbl text;
BEGIN
    FOR tbl IN
        SELECT c.relname
          FROM pg_class c
          JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE c.relkind IN ('r', 'p')
           AND n.nspname = 'public'
           AND EXISTS (
               SELECT 1
                 FROM pg_constraint con
                 JOIN pg_class parent ON parent.oid = con.confrelid
                WHERE con.conrelid = c.oid
                  AND con.contype = 'f'
                  AND parent.relnamespace = n.oid
                  AND parent.relname = 'teams'
           )
         ORDER BY c.relname
    LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tbl);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tbl);

        EXECUTE format('DROP POLICY IF EXISTS %I ON public.%I', tbl || '_team_select', tbl);
        EXECUTE format(
            'CREATE POLICY %I ON public.%I FOR SELECT USING (team_id = public.memoh_current_team_id())',
            tbl || '_team_select', tbl);

        EXECUTE format('DROP POLICY IF EXISTS %I ON public.%I', tbl || '_team_insert', tbl);
        EXECUTE format(
            'CREATE POLICY %I ON public.%I FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id())',
            tbl || '_team_insert', tbl);

        EXECUTE format('DROP POLICY IF EXISTS %I ON public.%I', tbl || '_team_update', tbl);
        EXECUTE format(
            'CREATE POLICY %I ON public.%I FOR UPDATE '
            || 'USING (team_id = public.memoh_current_team_id()) '
            || 'WITH CHECK (team_id = public.memoh_current_team_id())',
            tbl || '_team_update', tbl);

        EXECUTE format('DROP POLICY IF EXISTS %I ON public.%I', tbl || '_team_delete', tbl);
        EXECUTE format(
            'CREATE POLICY %I ON public.%I FOR DELETE USING (team_id = public.memoh_current_team_id())',
            tbl || '_team_delete', tbl);
    END LOOP;
END
$$;

ALTER TABLE public.teams ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.teams FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS teams_self_select ON public.teams;
CREATE POLICY teams_self_select ON public.teams
    FOR SELECT USING (id = public.memoh_current_team_id());

-- ---------------------------------------------------------------------------
-- Team-safe history view
-- ---------------------------------------------------------------------------
-- Fix the bot_visible_history_messages view so it cannot bypass team RLS.
--
-- The view was created without security_invoker, so it executed with its
-- owner's privileges and could bypass the caller's RLS policies. This migration:
--   1. recreates the view WITH (security_invoker = true) so it runs under the
--      caller's privileges — the base table's RLS then scopes it automatically;
--   2. projects team_id so consuming queries can carry explicit scope
--      (defense-in-depth) and so the schema guard can verify the view;

-- Adding team_id as the first projected column changes column order, which
-- CREATE OR REPLACE VIEW rejects, so drop and recreate. No other object depends
-- on this view (verified), so a plain DROP is safe.
DROP VIEW IF EXISTS bot_visible_history_messages;

CREATE VIEW bot_visible_history_messages
WITH (security_invoker = true) AS
SELECT
  m.team_id,
  m.turn_id,
  m.turn_position,
  m.turn_message_seq,
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id,
  m.source_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.compact_id,
  m.session_mode,
  m.runtime_type,
  m.model_id,
  m.event_id,
  m.display_text,
  m.created_at
FROM bot_history_messages m
WHERE m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Team-prefixed secondary indexes
-- ---------------------------------------------------------------------------
-- Prepend team_id to every non-unique btree secondary index on a team table
-- that does not already lead with it. Team queries filter by team_id (RLS +
-- explicit public.memoh_current_team_id()), so a team_id-leading index lets the
-- planner scan only the current team's slice. Purely a performance change.
--
-- New incremental (existing migrations untouched). Each index is dropped and
-- recreated with team_id prepended, preserving its column list, partial WHERE,
-- and (btree) access method. Non-btree (gin/gist) indexes are left untouched.

DO $$
DECLARE
    rec record;
    idxdef text;
BEGIN
    FOR rec IN
        SELECT ic.relname AS index_name, pg_get_indexdef(i.indexrelid) AS def
          FROM pg_index i
          JOIN pg_class ic ON ic.oid = i.indexrelid
          JOIN pg_class tc ON tc.oid = i.indrelid
          JOIN pg_namespace n ON n.oid = tc.relnamespace
          JOIN pg_am am ON am.oid = ic.relam
         WHERE n.nspname = 'public'
           AND NOT i.indisprimary AND NOT i.indisunique
           AND am.amname = 'btree'
           AND tc.relname <> 'team_members'
           AND EXISTS (
               SELECT 1
                 FROM pg_constraint con
                 JOIN pg_class parent ON parent.oid = con.confrelid
                WHERE con.conrelid = tc.oid
                  AND con.contype = 'f'
                  AND parent.relnamespace = n.oid
                  AND parent.relname = 'teams'
           )
           AND (SELECT attname FROM pg_attribute
                 WHERE attrelid = i.indrelid AND attnum = i.indkey[0]) <> 'team_id'
    LOOP
        idxdef := regexp_replace(rec.def, '(USING btree \()', '\1team_id, ');
        EXECUTE format('DROP INDEX IF EXISTS public.%I', rec.index_name);
        EXECUTE idxdef;
    END LOOP;
END
$$;

-- ---------------------------------------------------------------------------
-- Global principals and team memberships
-- ---------------------------------------------------------------------------
-- The migration owner may be a non-superuser. Disable the old FORCE RLS
-- boundary before inspecting every team; this file runs transactionally, so a
-- failed preflight restores the original policies and RLS state.
DROP POLICY IF EXISTS users_team_delete ON public.users;
DROP POLICY IF EXISTS users_team_update ON public.users;
DROP POLICY IF EXISTS users_team_insert ON public.users;
DROP POLICY IF EXISTS users_team_select ON public.users;
ALTER TABLE public.users NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.users DISABLE ROW LEVEL SECURITY;

-- A username or email could briefly have been created once per team after
-- 0112. Such rows cannot be merged automatically without choosing credentials
-- and ownership, so fail before changing constraints.
DO $duplicate_global_identities$
BEGIN
    IF EXISTS (
        SELECT 1 FROM public.users
         WHERE username IS NOT NULL
         GROUP BY username HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'cannot globalize users: duplicate usernames exist across teams';
    END IF;
    IF EXISTS (
        SELECT 1 FROM public.users
         WHERE email IS NOT NULL
         GROUP BY email HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'cannot globalize users: duplicate emails exist across teams';
    END IF;
END
$duplicate_global_identities$;

-- The canonical 0001 already creates this table with FORCE RLS. Temporarily
-- disable it so the replay path can rebuild foreign keys without requiring a
-- request-scoped team GUC. The legacy path reaches the same state with a new,
-- not-yet-protected table.
ALTER TABLE public.team_members NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.team_members DISABLE ROW LEVEL SECURITY;

INSERT INTO public.team_members (
    team_id, user_id, role, is_active, data_root, created_at, updated_at
)
SELECT
    (to_jsonb(u) ->> 'team_id')::uuid,
    u.id,
    (to_jsonb(u) ->> 'role')::user_role,
    u.is_active,
    to_jsonb(u) ->> 'data_root',
    u.created_at,
    u.updated_at
FROM public.users AS u
WHERE to_jsonb(u) ? 'team_id'
ON CONFLICT (team_id, user_id) DO NOTHING;

-- PostgreSQL validates replacement FKs by scanning their child tables. A
-- non-superuser migration owner is subject to FORCE RLS during that internal
-- scan, so temporarily suspend RLS while constraints are rebuilt.
ALTER TABLE public.bot_acl_rules NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_acl_rules DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_channel_admins NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_channel_admins DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_history_messages NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_history_messages DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_user_grants NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_user_grants DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.channel_link_codes NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.channel_link_codes DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.email_providers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.email_providers DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_channel_bindings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_channel_bindings DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_channel_identity_bindings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_channel_identity_bindings DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_provider_oauth_tokens NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_provider_oauth_tokens DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes DISABLE ROW LEVEL SECURITY;

-- Team-owned references now target membership rather than requiring the user
-- principal itself to belong to exactly one team.
ALTER TABLE public.bot_acl_rules
    DROP CONSTRAINT IF EXISTS bot_acl_rules_created_by_user_id_fkey;
ALTER TABLE public.bot_channel_admins
    DROP CONSTRAINT IF EXISTS bot_channel_admins_created_by_user_id_fkey;
ALTER TABLE public.bot_history_messages
    DROP CONSTRAINT IF EXISTS bot_history_messages_sender_account_user_id_fkey;
ALTER TABLE public.bot_sessions
    DROP CONSTRAINT IF EXISTS bot_sessions_created_by_user_id_fkey;
ALTER TABLE public.bot_user_grants
    DROP CONSTRAINT IF EXISTS bot_user_grants_created_by_user_id_fkey,
    DROP CONSTRAINT IF EXISTS bot_user_grants_user_id_fkey;
ALTER TABLE public.bots
    DROP CONSTRAINT IF EXISTS bots_owner_user_id_fkey;
ALTER TABLE public.channel_link_codes
    DROP CONSTRAINT IF EXISTS channel_link_codes_user_id_fkey;
ALTER TABLE public.email_providers
    DROP CONSTRAINT IF EXISTS email_providers_user_id_fkey;
ALTER TABLE public.user_channel_bindings
    DROP CONSTRAINT IF EXISTS user_channel_bindings_user_id_fkey;
ALTER TABLE public.user_channel_identity_bindings
    DROP CONSTRAINT IF EXISTS user_channel_identity_bindings_user_id_fkey;
ALTER TABLE public.user_provider_oauth_tokens
    DROP CONSTRAINT IF EXISTS user_provider_oauth_tokens_user_id_fkey;
ALTER TABLE public.user_runtimes
    DROP CONSTRAINT IF EXISTS user_runtimes_user_id_fkey;

ALTER TABLE public.bot_acl_rules
    ADD CONSTRAINT bot_acl_rules_created_by_user_id_fkey
    FOREIGN KEY (team_id, created_by_user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE SET NULL (created_by_user_id);
ALTER TABLE public.bot_channel_admins
    ADD CONSTRAINT bot_channel_admins_created_by_user_id_fkey
    FOREIGN KEY (team_id, created_by_user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE SET NULL (created_by_user_id);
ALTER TABLE public.bot_history_messages
    ADD CONSTRAINT bot_history_messages_sender_account_user_id_fkey
    FOREIGN KEY (team_id, sender_account_user_id)
    REFERENCES public.team_members(team_id, user_id);
ALTER TABLE public.bot_sessions
    ADD CONSTRAINT bot_sessions_created_by_user_id_fkey
    FOREIGN KEY (team_id, created_by_user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE SET NULL (created_by_user_id);
ALTER TABLE public.bot_user_grants
    ADD CONSTRAINT bot_user_grants_created_by_user_id_fkey
    FOREIGN KEY (team_id, created_by_user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE SET NULL (created_by_user_id),
    ADD CONSTRAINT bot_user_grants_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE CASCADE;
ALTER TABLE public.bots
    ADD CONSTRAINT bots_owner_user_id_fkey
    FOREIGN KEY (team_id, owner_user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE CASCADE;
ALTER TABLE public.channel_link_codes
    ADD CONSTRAINT channel_link_codes_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE CASCADE;
ALTER TABLE public.email_providers
    ADD CONSTRAINT email_providers_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE CASCADE;
ALTER TABLE public.user_channel_bindings
    ADD CONSTRAINT user_channel_bindings_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE CASCADE;
ALTER TABLE public.user_channel_identity_bindings
    ADD CONSTRAINT user_channel_identity_bindings_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE CASCADE;
ALTER TABLE public.user_provider_oauth_tokens
    ADD CONSTRAINT user_provider_oauth_tokens_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE CASCADE;
ALTER TABLE public.user_runtimes
    ADD CONSTRAINT user_runtimes_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE CASCADE;

ALTER TABLE public.bot_acl_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_acl_rules FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_channel_admins ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_channel_admins FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_history_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_history_messages FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_user_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_user_grants FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;
ALTER TABLE public.channel_link_codes ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.channel_link_codes FORCE ROW LEVEL SECURITY;
ALTER TABLE public.email_providers ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.email_providers FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_channel_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_channel_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_channel_identity_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_channel_identity_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_provider_oauth_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_provider_oauth_tokens FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes FORCE ROW LEVEL SECURITY;

ALTER TABLE public.users
    DROP CONSTRAINT IF EXISTS users_team_id_fkey,
    DROP CONSTRAINT IF EXISTS memoh_team_key_018c4edf45ca,
    DROP CONSTRAINT IF EXISTS users_email_unique,
    DROP CONSTRAINT IF EXISTS users_username_unique;

ALTER TABLE public.users
    DROP COLUMN IF EXISTS team_id,
    DROP COLUMN IF EXISTS role,
    DROP COLUMN IF EXISTS data_root;

ALTER TABLE public.users
    ADD CONSTRAINT users_email_unique UNIQUE (email),
    ADD CONSTRAINT users_username_unique UNIQUE (username);

ALTER TABLE public.team_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.team_members FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS team_members_team_select ON public.team_members;
DROP POLICY IF EXISTS team_members_team_insert ON public.team_members;
DROP POLICY IF EXISTS team_members_team_update ON public.team_members;
DROP POLICY IF EXISTS team_members_team_delete ON public.team_members;
CREATE POLICY team_members_team_select ON public.team_members
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY team_members_team_insert ON public.team_members
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY team_members_team_update ON public.team_members
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY team_members_team_delete ON public.team_members
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- Preserve the account-shaped query contract while sourcing authorization
-- fields from the current team's membership.
CREATE OR REPLACE VIEW public.team_accounts
WITH (security_invoker = true)
AS
SELECT
    u.id,
    u.username,
    u.email,
    u.password_hash,
    tm.role,
    u.display_name,
    u.avatar_url,
    u.timezone,
    tm.data_root,
    u.last_login_at,
    (u.is_active AND tm.is_active) AS is_active,
    u.metadata,
    u.created_at,
    u.updated_at,
    tm.team_id,
    u.is_active AS principal_is_active,
    tm.is_active AS membership_is_active,
    tm.created_at AS joined_at,
    tm.updated_at AS membership_updated_at,
    tm.title_model_id
FROM public.team_members tm
JOIN public.users u ON u.id = tm.user_id
WHERE tm.team_id = public.memoh_current_team_id();

-- Serialize membership authority changes and keep one active admin per team.
CREATE OR REPLACE FUNCTION public.memoh_guard_last_active_team_admin()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF OLD.role <> 'admin'
       OR NOT OLD.is_active
       OR NOT EXISTS (
           SELECT 1
             FROM public.users principal
            WHERE principal.id = OLD.user_id
              AND principal.is_active
       ) THEN
        IF TG_OP = 'DELETE' THEN
            RETURN OLD;
        END IF;
        RETURN NEW;
    END IF;

    IF TG_OP = 'UPDATE' AND NEW.role = 'admin' AND NEW.is_active THEN
        RETURN NEW;
    END IF;

    -- Concurrent demotions/removals for the same team must observe each other.
    PERFORM 1
      FROM public.teams
     WHERE id = OLD.team_id
     FOR UPDATE;

    IF NOT EXISTS (
        SELECT 1
          FROM public.team_members candidate
          JOIN public.users principal
            ON principal.id = candidate.user_id
           AND principal.is_active
         WHERE candidate.team_id = OLD.team_id
           AND candidate.user_id <> OLD.user_id
           AND candidate.role = 'admin'
           AND candidate.is_active
    ) THEN
        RAISE EXCEPTION 'team must retain at least one active admin'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'team_members_last_active_admin';
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS team_members_last_active_admin_guard ON public.team_members;
CREATE TRIGGER team_members_last_active_admin_guard
BEFORE UPDATE OF role, is_active OR DELETE ON public.team_members
FOR EACH ROW
EXECUTE FUNCTION public.memoh_guard_last_active_team_admin();

-- A fresh install replays the historical incremental migrations on the same
-- golang-migrate connection. Bind that short-lived connection to the singleton
-- team so data-rewrite statements remain valid under the canonical FORCE RLS
-- schema. The connection is closed after migration; ordinary runtime
-- connections still fail closed until they bind memoh.team_id themselves.
SELECT set_config(
    'memoh.team_id',
    '00000000-0000-0000-0000-000000000001',
    false
);

-- Managed subagents pin their selected model. Forked model context is stored
-- as scoped invisible rows in bot_history_messages.
CREATE TABLE IF NOT EXISTS public.subagent_configs (
    team_id         UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                REFERENCES public.teams(id) ON DELETE RESTRICT,
    session_id      UUID        PRIMARY KEY,
    model_uuid      UUID,
    model_id        TEXT        NOT NULL,
    provider_name   TEXT        NOT NULL,
    forked          BOOLEAN     NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT subagent_configs_team_session_key UNIQUE (team_id, session_id),
    CONSTRAINT subagent_configs_session_id_fkey
        FOREIGN KEY (team_id, session_id)
        REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE,
    CONSTRAINT subagent_configs_model_uuid_fkey
        FOREIGN KEY (team_id, model_uuid)
        REFERENCES public.models(team_id, id) ON DELETE SET NULL (model_uuid)
);

CREATE INDEX IF NOT EXISTS idx_subagent_configs_team_model
    ON public.subagent_configs (team_id, model_uuid);

ALTER TABLE public.subagent_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.subagent_configs FORCE ROW LEVEL SECURITY;

CREATE POLICY subagent_configs_team_select ON public.subagent_configs
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY subagent_configs_team_insert ON public.subagent_configs
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY subagent_configs_team_update ON public.subagent_configs
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY subagent_configs_team_delete ON public.subagent_configs
    FOR DELETE USING (team_id = public.memoh_current_team_id());
