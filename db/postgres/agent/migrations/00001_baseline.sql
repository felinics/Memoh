-- +goose Up
-- +goose StatementBegin
-- Goose SessionLock may SET ROLE memoh_migrate before this script runs.
-- Reset to the deploying login so this owner can normalize its schema and ledger.
RESET ROLE;

CREATE SCHEMA IF NOT EXISTS agent;
ALTER SCHEMA agent OWNER TO memoh_migrate;

DO $defaults_login$
DECLARE
  login_role text := session_user;
BEGIN
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA agent GRANT ALL ON TABLES TO memoh_migrate', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA agent GRANT ALL ON SEQUENCES TO memoh_migrate', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA agent GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO memoh_agent', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA agent GRANT USAGE, SELECT ON SEQUENCES TO memoh_agent', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA agent GRANT EXECUTE ON FUNCTIONS TO memoh_agent', login_role);
END
$defaults_login$;

DO $version_owner$
BEGIN
  IF to_regclass('agent.goose_db_version') IS NOT NULL THEN
    ALTER TABLE agent.goose_db_version OWNER TO memoh_migrate;
  END IF;
END
$version_owner$;

SET LOCAL ROLE memoh_migrate;

REVOKE ALL ON SCHEMA agent FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA agent TO memoh_migrate;
GRANT USAGE ON SCHEMA agent TO memoh_agent;

ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA agent GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO memoh_agent;
ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA agent GRANT USAGE, SELECT ON SEQUENCES TO memoh_agent;
ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA agent GRANT EXECUTE ON FUNCTIONS TO memoh_agent;

CREATE TABLE agent.bot_heartbeat_logs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    session_id uuid,
    status text DEFAULT 'ok'::text NOT NULL,
    result_text text DEFAULT ''::text NOT NULL,
    error_message text DEFAULT ''::text NOT NULL,
    usage jsonb,
    model_id uuid,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT bot_heartbeat_logs_status_check CHECK ((status = ANY (ARRAY['ok'::text, 'alert'::text, 'error'::text])))
);

ALTER TABLE ONLY agent.bot_heartbeat_logs FORCE ROW LEVEL SECURITY;

CREATE TABLE agent.bot_history_message_assets (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    message_id uuid NOT NULL,
    role text DEFAULT 'attachment'::text NOT NULL,
    ordinal integer DEFAULT 0 NOT NULL,
    content_hash text NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY agent.bot_history_message_assets FORCE ROW LEVEL SECURITY;

CREATE TABLE agent.bot_history_message_compacts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    session_id uuid,
    status text DEFAULT 'pending'::text NOT NULL,
    summary text DEFAULT ''::text NOT NULL,
    message_count integer DEFAULT 0 NOT NULL,
    error_message text DEFAULT ''::text NOT NULL,
    usage jsonb,
    model_id uuid,
    artifact_version integer DEFAULT 1 NOT NULL,
    coverage jsonb DEFAULT '[]'::jsonb NOT NULL,
    anchor_start_ms bigint DEFAULT 0 NOT NULL,
    anchor_end_ms bigint DEFAULT 0 NOT NULL,
    artifact_level integer DEFAULT 0 NOT NULL,
    parent_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    superseded_by uuid,
    superseded_at timestamp with time zone,
    compaction_epoch bigint DEFAULT 0 NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT bot_history_message_compacts_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'ok'::text, 'error'::text])))
);

ALTER TABLE ONLY agent.bot_history_message_compacts FORCE ROW LEVEL SECURITY;

CREATE TABLE agent.bot_history_messages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    session_id uuid,
    sender_channel_identity_id uuid,
    sender_account_user_id uuid,
    sender_display_name text,
    sender_avatar_url text,
    source_message_id text,
    source_reply_to_message_id text,
    role text NOT NULL,
    content jsonb NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    usage jsonb,
    session_mode text DEFAULT 'chat'::text NOT NULL,
    runtime_type text DEFAULT 'model'::text NOT NULL,
    model_id uuid,
    compact_id uuid,
    event_id uuid,
    display_text text,
    turn_id uuid,
    turn_position bigint,
    turn_message_seq bigint,
    turn_visible boolean DEFAULT false NOT NULL,
    turn_superseded_by_turn_id uuid,
    turn_superseded_at timestamp with time zone,
    turn_superseded_reason text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT bot_history_messages_role_check CHECK ((role = ANY (ARRAY['user'::text, 'assistant'::text, 'system'::text, 'tool'::text]))),
    CONSTRAINT bot_history_messages_runtime_type_check CHECK ((runtime_type = ANY (ARRAY['model'::text, 'acp_agent'::text]))),
    CONSTRAINT bot_history_messages_session_mode_check CHECK ((session_mode = ANY (ARRAY['chat'::text, 'discuss'::text, 'heartbeat'::text, 'schedule'::text, 'subagent'::text])))
);

ALTER TABLE ONLY agent.bot_history_messages FORCE ROW LEVEL SECURITY;

CREATE TABLE agent.bot_plugin_installations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    plugin_id text NOT NULL,
    plugin_name text DEFAULT ''::text NOT NULL,
    version text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'ready'::text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    manifest jsonb DEFAULT '{}'::jsonb NOT NULL,
    installed_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY agent.bot_plugin_installations FORCE ROW LEVEL SECURITY;

CREATE TABLE agent.bot_plugin_resources (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    installation_id uuid NOT NULL,
    resource_type text NOT NULL,
    resource_key text NOT NULL,
    resource_id text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY agent.bot_plugin_resources FORCE ROW LEVEL SECURITY;

CREATE TABLE agent.bot_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    route_id uuid,
    channel_type text,
    conversation_type text,
    conversation_name text,
    reply_target text,
    type text DEFAULT 'chat'::text NOT NULL,
    session_mode text DEFAULT 'chat'::text NOT NULL,
    runtime_type text DEFAULT 'model'::text NOT NULL,
    runtime_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    next_turn_position bigint DEFAULT 1 NOT NULL,
    compaction_epoch bigint DEFAULT 0 NOT NULL,
    runtime_fencing_token bigint DEFAULT 0 NOT NULL,
    parent_session_id uuid,
    created_by_user_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT bot_sessions_runtime_fencing_token_check CHECK ((runtime_fencing_token >= 0)),
    CONSTRAINT bot_sessions_runtime_type_check CHECK ((runtime_type = ANY (ARRAY['model'::text, 'acp_agent'::text]))),
    CONSTRAINT bot_sessions_session_mode_check CHECK ((session_mode = ANY (ARRAY['chat'::text, 'discuss'::text, 'heartbeat'::text, 'schedule'::text, 'subagent'::text]))),
    CONSTRAINT bot_sessions_type_check CHECK ((type = ANY (ARRAY['chat'::text, 'heartbeat'::text, 'schedule'::text, 'subagent'::text, 'discuss'::text, 'acp_agent'::text])))
);

ALTER TABLE ONLY agent.bot_sessions FORCE ROW LEVEL SECURITY;

CREATE VIEW agent.bot_visible_history_messages WITH (security_invoker='true') AS
 SELECT team_id,
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
   FROM agent.bot_history_messages m
  WHERE ((turn_visible = true) AND (turn_id IS NOT NULL) AND (turn_position IS NOT NULL) AND (turn_message_seq IS NOT NULL));

CREATE TABLE agent.mcp_connections (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    name text NOT NULL,
    type text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    status text DEFAULT 'unknown'::text NOT NULL,
    tools_cache jsonb DEFAULT '[]'::jsonb NOT NULL,
    last_probed_at timestamp with time zone,
    status_message text DEFAULT ''::text NOT NULL,
    auth_type text DEFAULT 'none'::text NOT NULL,
    managed_by_plugin_installation_id uuid,
    managed_resource_key text DEFAULT ''::text NOT NULL,
    visible boolean DEFAULT true NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT mcp_connections_type_check CHECK ((type = ANY (ARRAY['stdio'::text, 'http'::text, 'sse'::text])))
);

ALTER TABLE ONLY agent.mcp_connections FORCE ROW LEVEL SECURITY;

CREATE TABLE agent.mcp_oauth_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    connection_id uuid NOT NULL,
    resource_metadata_url text DEFAULT ''::text NOT NULL,
    authorization_server_url text DEFAULT ''::text NOT NULL,
    authorization_endpoint text DEFAULT ''::text NOT NULL,
    token_endpoint text DEFAULT ''::text NOT NULL,
    registration_endpoint text DEFAULT ''::text NOT NULL,
    scopes_supported text[] DEFAULT '{}'::text[] NOT NULL,
    client_id text DEFAULT ''::text NOT NULL,
    client_secret text DEFAULT ''::text NOT NULL,
    access_token text DEFAULT ''::text NOT NULL,
    refresh_token text DEFAULT ''::text NOT NULL,
    token_type text DEFAULT 'Bearer'::text NOT NULL,
    expires_at timestamp with time zone,
    scope text DEFAULT ''::text NOT NULL,
    pkce_code_verifier text DEFAULT ''::text NOT NULL,
    state_param text DEFAULT ''::text NOT NULL,
    resource_uri text DEFAULT ''::text NOT NULL,
    redirect_uri text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY agent.mcp_oauth_tokens FORCE ROW LEVEL SECURITY;

CREATE TABLE agent.schedule (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    description text NOT NULL,
    pattern text NOT NULL,
    max_calls integer,
    current_calls integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    command text NOT NULL,
    bot_id uuid NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY agent.schedule FORCE ROW LEVEL SECURITY;

CREATE TABLE agent.schedule_logs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    schedule_id uuid NOT NULL,
    bot_id uuid NOT NULL,
    session_id uuid,
    status text DEFAULT 'ok'::text NOT NULL,
    result_text text DEFAULT ''::text NOT NULL,
    error_message text DEFAULT ''::text NOT NULL,
    usage jsonb,
    model_id uuid,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT schedule_logs_status_check CHECK ((status = ANY (ARRAY['ok'::text, 'error'::text])))
);

ALTER TABLE ONLY agent.schedule_logs FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE agent.session_runtime_fencing_token_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE agent.subagent_configs (
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    session_id uuid NOT NULL,
    model_uuid uuid,
    model_id text NOT NULL,
    provider_name text NOT NULL,
    forked boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY agent.subagent_configs FORCE ROW LEVEL SECURITY;

CREATE TABLE agent.tool_approval_requests (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    session_id uuid NOT NULL,
    route_id uuid,
    channel_identity_id uuid,
    workspace_target_id text DEFAULT ''::text NOT NULL,
    tool_call_id text NOT NULL,
    tool_name text NOT NULL,
    operation text NOT NULL,
    tool_input jsonb NOT NULL,
    short_id integer NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    runtime_fencing_token bigint,
    decision_reason text DEFAULT ''::text NOT NULL,
    requested_by_channel_identity_id uuid,
    decided_by_channel_identity_id uuid,
    requested_message_id uuid,
    prompt_message_id uuid,
    prompt_external_message_id text DEFAULT ''::text NOT NULL,
    source_platform text DEFAULT ''::text NOT NULL,
    reply_target text DEFAULT ''::text NOT NULL,
    conversation_type text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    decided_at timestamp with time zone,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT tool_approval_operation_check CHECK ((operation = ANY (ARRAY['read'::text, 'write'::text, 'exec'::text]))),
    CONSTRAINT tool_approval_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text, 'expired'::text, 'cancelled'::text])))
);

ALTER TABLE ONLY agent.tool_approval_requests FORCE ROW LEVEL SECURITY;

CREATE TABLE agent.user_input_requests (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    session_id uuid NOT NULL,
    route_id uuid,
    channel_identity_id uuid,
    workspace_target_id text DEFAULT ''::text NOT NULL,
    tool_call_id text NOT NULL,
    tool_name text DEFAULT 'ask_user'::text NOT NULL,
    short_id integer NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    runtime_fencing_token bigint,
    input_json jsonb NOT NULL,
    ui_payload_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    interaction_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    interaction_revision integer DEFAULT 0 NOT NULL,
    result_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    provider_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    requested_by_channel_identity_id uuid,
    responded_by_channel_identity_id uuid,
    assistant_message_id uuid,
    tool_result_message_id uuid,
    prompt_message_id uuid,
    prompt_external_message_id text DEFAULT ''::text NOT NULL,
    source_platform text DEFAULT ''::text NOT NULL,
    reply_target text DEFAULT ''::text NOT NULL,
    conversation_type text DEFAULT ''::text NOT NULL,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    responded_at timestamp with time zone,
    canceled_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT user_input_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'submitted'::text, 'canceled'::text, 'expired'::text, 'failed'::text]))),
    CONSTRAINT user_input_tool_name_check CHECK ((tool_name = 'ask_user'::text))
);

ALTER TABLE ONLY agent.user_input_requests FORCE ROW LEVEL SECURITY;

ALTER TABLE ONLY agent.bot_heartbeat_logs
    ADD CONSTRAINT bot_heartbeat_logs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY agent.bot_history_message_assets
    ADD CONSTRAINT bot_history_message_assets_pkey PRIMARY KEY (id);

ALTER TABLE ONLY agent.bot_history_message_compacts
    ADD CONSTRAINT bot_history_message_compacts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY agent.bot_history_messages
    ADD CONSTRAINT bot_history_messages_pkey PRIMARY KEY (id);

ALTER TABLE ONLY agent.bot_plugin_installations
    ADD CONSTRAINT bot_plugin_installations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY agent.bot_plugin_installations
    ADD CONSTRAINT bot_plugin_installations_unique UNIQUE (team_id, bot_id, plugin_id);

ALTER TABLE ONLY agent.bot_plugin_resources
    ADD CONSTRAINT bot_plugin_resources_pkey PRIMARY KEY (id);

ALTER TABLE ONLY agent.bot_plugin_resources
    ADD CONSTRAINT bot_plugin_resources_unique UNIQUE (team_id, installation_id, resource_type, resource_key);

ALTER TABLE ONLY agent.bot_sessions
    ADD CONSTRAINT bot_sessions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY agent.mcp_connections
    ADD CONSTRAINT mcp_connections_pkey PRIMARY KEY (id);

ALTER TABLE ONLY agent.mcp_connections
    ADD CONSTRAINT mcp_connections_unique UNIQUE (team_id, bot_id, name);

ALTER TABLE ONLY agent.mcp_oauth_tokens
    ADD CONSTRAINT mcp_oauth_tokens_connection_id_key UNIQUE (team_id, connection_id);

ALTER TABLE ONLY agent.mcp_oauth_tokens
    ADD CONSTRAINT mcp_oauth_tokens_pkey PRIMARY KEY (id);

ALTER TABLE ONLY agent.mcp_oauth_tokens
    ADD CONSTRAINT memoh_team_key_02f43ca206ff UNIQUE (team_id, id);

ALTER TABLE ONLY agent.user_input_requests
    ADD CONSTRAINT memoh_team_key_25351826f141 UNIQUE (team_id, id);

ALTER TABLE ONLY agent.bot_plugin_resources
    ADD CONSTRAINT memoh_team_key_288ff97bb452 UNIQUE (team_id, id);

ALTER TABLE ONLY agent.mcp_connections
    ADD CONSTRAINT memoh_team_key_3a7ac918eb09 UNIQUE (team_id, id);

ALTER TABLE ONLY agent.bot_plugin_installations
    ADD CONSTRAINT memoh_team_key_640e5c8de6b5 UNIQUE (team_id, id);

ALTER TABLE ONLY agent.bot_history_messages
    ADD CONSTRAINT memoh_team_key_6e575bdde4a7 UNIQUE (team_id, id);

ALTER TABLE ONLY agent.bot_heartbeat_logs
    ADD CONSTRAINT memoh_team_key_81f61b099ffc UNIQUE (team_id, id);

ALTER TABLE ONLY agent.schedule_logs
    ADD CONSTRAINT memoh_team_key_8dced8a10318 UNIQUE (team_id, id);

ALTER TABLE ONLY agent.schedule
    ADD CONSTRAINT memoh_team_key_a305907efebe UNIQUE (team_id, id);

ALTER TABLE ONLY agent.subagent_configs
    ADD CONSTRAINT memoh_team_key_a324e2f569ff UNIQUE (team_id, session_id);

ALTER TABLE ONLY agent.bot_sessions
    ADD CONSTRAINT memoh_team_key_a39f09c8972a UNIQUE (team_id, id);

ALTER TABLE ONLY agent.tool_approval_requests
    ADD CONSTRAINT memoh_team_key_b54d8d1ccdec UNIQUE (team_id, id);

ALTER TABLE ONLY agent.bot_history_message_assets
    ADD CONSTRAINT memoh_team_key_c7ffe2b0b93c UNIQUE (team_id, id);

ALTER TABLE ONLY agent.bot_history_message_compacts
    ADD CONSTRAINT memoh_team_key_dbb8be153799 UNIQUE (team_id, id);

ALTER TABLE ONLY agent.bot_history_message_assets
    ADD CONSTRAINT message_asset_content_unique UNIQUE (team_id, message_id, content_hash);

ALTER TABLE ONLY agent.schedule_logs
    ADD CONSTRAINT schedule_logs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY agent.schedule
    ADD CONSTRAINT schedule_pkey PRIMARY KEY (id);

ALTER TABLE ONLY agent.subagent_configs
    ADD CONSTRAINT subagent_configs_pkey PRIMARY KEY (session_id);

ALTER TABLE ONLY agent.subagent_configs
    ADD CONSTRAINT subagent_configs_team_session_key UNIQUE (team_id, session_id);

ALTER TABLE ONLY agent.tool_approval_requests
    ADD CONSTRAINT tool_approval_requests_pkey PRIMARY KEY (id);

ALTER TABLE ONLY agent.tool_approval_requests
    ADD CONSTRAINT tool_approval_short_id_unique UNIQUE (team_id, session_id, short_id);

ALTER TABLE ONLY agent.tool_approval_requests
    ADD CONSTRAINT tool_approval_tool_call_unique UNIQUE (team_id, session_id, tool_call_id);

ALTER TABLE ONLY agent.user_input_requests
    ADD CONSTRAINT user_input_requests_pkey PRIMARY KEY (id);

ALTER TABLE ONLY agent.user_input_requests
    ADD CONSTRAINT user_input_short_id_unique UNIQUE (team_id, session_id, short_id);

ALTER TABLE ONLY agent.user_input_requests
    ADD CONSTRAINT user_input_tool_call_unique UNIQUE (team_id, session_id, tool_call_id);

CREATE INDEX idx_bot_history_messages_bot_created ON agent.bot_history_messages USING btree (team_id, bot_id, created_at);

CREATE INDEX idx_bot_history_messages_compact ON agent.bot_history_messages USING btree (team_id, compact_id);

CREATE INDEX idx_bot_history_messages_session ON agent.bot_history_messages USING btree (team_id, session_id, created_at);

CREATE INDEX idx_bot_history_messages_session_reply ON agent.bot_history_messages USING btree (team_id, session_id, source_reply_to_message_id);

CREATE INDEX idx_bot_history_messages_session_role_created ON agent.bot_history_messages USING btree (team_id, session_id, role, created_at, id);

CREATE INDEX idx_bot_history_messages_session_source ON agent.bot_history_messages USING btree (team_id, session_id, source_message_id);

CREATE INDEX idx_bot_history_messages_subagent_fork_context ON agent.bot_history_messages USING btree (team_id, session_id, turn_position, turn_message_seq) WHERE ((turn_visible = false) AND (session_mode = 'subagent'::text) AND ((metadata ->> 'context_scope'::text) = 'subagent_fork'::text));

CREATE INDEX idx_bot_history_messages_turn ON agent.bot_history_messages USING btree (team_id, turn_id, turn_message_seq, created_at, id) WHERE (turn_id IS NOT NULL);

CREATE UNIQUE INDEX idx_bot_history_messages_turn_seq_unique ON agent.bot_history_messages USING btree (team_id, turn_id, turn_message_seq) WHERE ((turn_id IS NOT NULL) AND (turn_message_seq IS NOT NULL));

CREATE INDEX idx_bot_history_messages_visible_session_order ON agent.bot_history_messages USING btree (team_id, session_id, turn_position DESC, turn_message_seq DESC, created_at DESC, id DESC) WHERE ((turn_visible = true) AND (turn_id IS NOT NULL) AND (turn_position IS NOT NULL) AND (turn_message_seq IS NOT NULL));

CREATE INDEX idx_bot_history_messages_visible_session_source_order ON agent.bot_history_messages USING btree (team_id, session_id, source_message_id, turn_position DESC, turn_message_seq DESC, created_at DESC, id DESC) WHERE ((turn_visible = true) AND (source_message_id IS NOT NULL) AND (turn_id IS NOT NULL) AND (turn_position IS NOT NULL) AND (turn_message_seq IS NOT NULL));

CREATE INDEX idx_bot_plugin_installations_bot_id ON agent.bot_plugin_installations USING btree (team_id, bot_id);

CREATE INDEX idx_bot_plugin_installations_plugin_id ON agent.bot_plugin_installations USING btree (team_id, plugin_id);

CREATE INDEX idx_bot_plugin_resources_installation_id ON agent.bot_plugin_resources USING btree (team_id, installation_id);

CREATE INDEX idx_bot_plugin_resources_resource ON agent.bot_plugin_resources USING btree (team_id, resource_type, resource_id);

CREATE INDEX idx_bot_sessions_bot_active ON agent.bot_sessions USING btree (team_id, bot_id, deleted_at);

CREATE INDEX idx_bot_sessions_bot_active_updated ON agent.bot_sessions USING btree (team_id, bot_id, updated_at DESC, id DESC) WHERE (deleted_at IS NULL);

CREATE INDEX idx_bot_sessions_bot_created_by ON agent.bot_sessions USING btree (team_id, bot_id, created_by_user_id, deleted_at);

CREATE INDEX idx_bot_sessions_bot_id ON agent.bot_sessions USING btree (team_id, bot_id);

CREATE INDEX idx_bot_sessions_bot_mode_runtime_active_updated ON agent.bot_sessions USING btree (team_id, bot_id, session_mode, runtime_type, updated_at DESC, id DESC) WHERE (deleted_at IS NULL);

CREATE INDEX idx_bot_sessions_created_by_user_id ON agent.bot_sessions USING btree (team_id, created_by_user_id) WHERE (created_by_user_id IS NOT NULL);

CREATE INDEX idx_bot_sessions_parent ON agent.bot_sessions USING btree (team_id, parent_session_id) WHERE (parent_session_id IS NOT NULL);

CREATE INDEX idx_bot_sessions_route_id ON agent.bot_sessions USING btree (team_id, route_id);

CREATE INDEX idx_compacts_active_session ON agent.bot_history_message_compacts USING btree (team_id, session_id, anchor_start_ms, started_at) WHERE ((status = 'ok'::text) AND (superseded_at IS NULL));

CREATE INDEX idx_compacts_bot_session ON agent.bot_history_message_compacts USING btree (team_id, bot_id, session_id, started_at DESC);

CREATE INDEX idx_compacts_owner_epoch ON agent.bot_history_message_compacts USING btree (team_id, bot_id, session_id, compaction_epoch, started_at DESC);

CREATE INDEX idx_heartbeat_logs_bot_started ON agent.bot_heartbeat_logs USING btree (team_id, bot_id, started_at DESC);

CREATE INDEX idx_mcp_connections_bot_id ON agent.mcp_connections USING btree (team_id, bot_id);

CREATE INDEX idx_mcp_connections_plugin_installation_id ON agent.mcp_connections USING btree (team_id, managed_by_plugin_installation_id);

CREATE INDEX idx_mcp_oauth_tokens_connection_id ON agent.mcp_oauth_tokens USING btree (team_id, connection_id);

CREATE INDEX idx_message_assets_message_id ON agent.bot_history_message_assets USING btree (team_id, message_id);

CREATE INDEX idx_schedule_bot_id ON agent.schedule USING btree (team_id, bot_id);

CREATE INDEX idx_schedule_enabled ON agent.schedule USING btree (team_id, enabled);

CREATE INDEX idx_schedule_logs_bot ON agent.schedule_logs USING btree (team_id, bot_id, started_at DESC);

CREATE INDEX idx_schedule_logs_schedule ON agent.schedule_logs USING btree (team_id, schedule_id, started_at DESC);

CREATE INDEX idx_subagent_configs_team_model ON agent.subagent_configs USING btree (team_id, model_uuid);

CREATE INDEX idx_tool_approval_bot_status_created ON agent.tool_approval_requests USING btree (team_id, bot_id, status, created_at);

CREATE INDEX idx_tool_approval_prompt_external ON agent.tool_approval_requests USING btree (team_id, prompt_external_message_id) WHERE (prompt_external_message_id <> ''::text);

CREATE INDEX idx_tool_approval_session_status_created ON agent.tool_approval_requests USING btree (team_id, session_id, status, created_at);

CREATE INDEX idx_user_input_bot_status_created ON agent.user_input_requests USING btree (team_id, bot_id, status, created_at);

CREATE INDEX idx_user_input_prompt_external ON agent.user_input_requests USING btree (team_id, prompt_external_message_id) WHERE (prompt_external_message_id <> ''::text);

CREATE INDEX idx_user_input_session_status_created ON agent.user_input_requests USING btree (team_id, session_id, status, created_at);

ALTER TABLE ONLY agent.bot_heartbeat_logs
    ADD CONSTRAINT bot_heartbeat_logs_session_id_fkey FOREIGN KEY (team_id, session_id) REFERENCES agent.bot_sessions(team_id, id) ON DELETE SET NULL (session_id);

ALTER TABLE ONLY agent.bot_history_message_assets
    ADD CONSTRAINT bot_history_message_assets_message_id_fkey FOREIGN KEY (team_id, message_id) REFERENCES agent.bot_history_messages(team_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY agent.bot_history_message_compacts
    ADD CONSTRAINT bot_history_message_compacts_session_id_fkey FOREIGN KEY (team_id, session_id) REFERENCES agent.bot_sessions(team_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY agent.bot_history_message_compacts
    ADD CONSTRAINT bot_history_message_compacts_superseded_by_fkey FOREIGN KEY (team_id, superseded_by) REFERENCES agent.bot_history_message_compacts(team_id, id) ON DELETE SET NULL (superseded_by);

ALTER TABLE ONLY agent.bot_history_messages
    ADD CONSTRAINT bot_history_messages_session_id_fkey FOREIGN KEY (team_id, session_id) REFERENCES agent.bot_sessions(team_id, id) ON DELETE SET NULL (session_id);

ALTER TABLE ONLY agent.bot_plugin_resources
    ADD CONSTRAINT bot_plugin_resources_installation_id_fkey FOREIGN KEY (team_id, installation_id) REFERENCES agent.bot_plugin_installations(team_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY agent.bot_sessions
    ADD CONSTRAINT bot_sessions_parent_session_id_fkey FOREIGN KEY (team_id, parent_session_id) REFERENCES agent.bot_sessions(team_id, id) ON DELETE SET NULL (parent_session_id);

ALTER TABLE ONLY agent.bot_history_messages
    ADD CONSTRAINT fk_compact_id FOREIGN KEY (team_id, compact_id) REFERENCES agent.bot_history_message_compacts(team_id, id) ON DELETE SET NULL (compact_id);

ALTER TABLE ONLY agent.mcp_connections
    ADD CONSTRAINT mcp_connections_managed_by_plugin_installation_id_fkey FOREIGN KEY (team_id, managed_by_plugin_installation_id) REFERENCES agent.bot_plugin_installations(team_id, id) ON DELETE SET NULL (managed_by_plugin_installation_id);

ALTER TABLE ONLY agent.mcp_oauth_tokens
    ADD CONSTRAINT mcp_oauth_tokens_connection_id_fkey FOREIGN KEY (team_id, connection_id) REFERENCES agent.mcp_connections(team_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY agent.schedule_logs
    ADD CONSTRAINT schedule_logs_schedule_id_fkey FOREIGN KEY (team_id, schedule_id) REFERENCES agent.schedule(team_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY agent.schedule_logs
    ADD CONSTRAINT schedule_logs_session_id_fkey FOREIGN KEY (team_id, session_id) REFERENCES agent.bot_sessions(team_id, id) ON DELETE SET NULL (session_id);

ALTER TABLE ONLY agent.subagent_configs
    ADD CONSTRAINT subagent_configs_session_id_fkey FOREIGN KEY (team_id, session_id) REFERENCES agent.bot_sessions(team_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY agent.tool_approval_requests
    ADD CONSTRAINT tool_approval_requests_prompt_message_id_fkey FOREIGN KEY (team_id, prompt_message_id) REFERENCES agent.bot_history_messages(team_id, id) ON DELETE SET NULL (prompt_message_id);

ALTER TABLE ONLY agent.tool_approval_requests
    ADD CONSTRAINT tool_approval_requests_requested_message_id_fkey FOREIGN KEY (team_id, requested_message_id) REFERENCES agent.bot_history_messages(team_id, id) ON DELETE SET NULL (requested_message_id);

ALTER TABLE ONLY agent.tool_approval_requests
    ADD CONSTRAINT tool_approval_requests_session_id_fkey FOREIGN KEY (team_id, session_id) REFERENCES agent.bot_sessions(team_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY agent.user_input_requests
    ADD CONSTRAINT user_input_requests_assistant_message_id_fkey FOREIGN KEY (team_id, assistant_message_id) REFERENCES agent.bot_history_messages(team_id, id) ON DELETE SET NULL (assistant_message_id);

ALTER TABLE ONLY agent.user_input_requests
    ADD CONSTRAINT user_input_requests_prompt_message_id_fkey FOREIGN KEY (team_id, prompt_message_id) REFERENCES agent.bot_history_messages(team_id, id) ON DELETE SET NULL (prompt_message_id);

ALTER TABLE ONLY agent.user_input_requests
    ADD CONSTRAINT user_input_requests_session_id_fkey FOREIGN KEY (team_id, session_id) REFERENCES agent.bot_sessions(team_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY agent.user_input_requests
    ADD CONSTRAINT user_input_requests_tool_result_message_id_fkey FOREIGN KEY (team_id, tool_result_message_id) REFERENCES agent.bot_history_messages(team_id, id) ON DELETE SET NULL (tool_result_message_id);

ALTER TABLE agent.bot_heartbeat_logs ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_heartbeat_logs_team_delete ON agent.bot_heartbeat_logs FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_heartbeat_logs_team_insert ON agent.bot_heartbeat_logs FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_heartbeat_logs_team_select ON agent.bot_heartbeat_logs FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_heartbeat_logs_team_update ON agent.bot_heartbeat_logs FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE agent.bot_history_message_assets ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_history_message_assets_team_delete ON agent.bot_history_message_assets FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_history_message_assets_team_insert ON agent.bot_history_message_assets FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_history_message_assets_team_select ON agent.bot_history_message_assets FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_history_message_assets_team_update ON agent.bot_history_message_assets FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE agent.bot_history_message_compacts ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_history_message_compacts_team_delete ON agent.bot_history_message_compacts FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_history_message_compacts_team_insert ON agent.bot_history_message_compacts FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_history_message_compacts_team_select ON agent.bot_history_message_compacts FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_history_message_compacts_team_update ON agent.bot_history_message_compacts FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE agent.bot_history_messages ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_history_messages_team_delete ON agent.bot_history_messages FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_history_messages_team_insert ON agent.bot_history_messages FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_history_messages_team_select ON agent.bot_history_messages FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_history_messages_team_update ON agent.bot_history_messages FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE agent.bot_plugin_installations ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_plugin_installations_team_delete ON agent.bot_plugin_installations FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_plugin_installations_team_insert ON agent.bot_plugin_installations FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_plugin_installations_team_select ON agent.bot_plugin_installations FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_plugin_installations_team_update ON agent.bot_plugin_installations FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE agent.bot_plugin_resources ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_plugin_resources_team_delete ON agent.bot_plugin_resources FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_plugin_resources_team_insert ON agent.bot_plugin_resources FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_plugin_resources_team_select ON agent.bot_plugin_resources FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_plugin_resources_team_update ON agent.bot_plugin_resources FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE agent.bot_sessions ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_sessions_team_delete ON agent.bot_sessions FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_sessions_team_insert ON agent.bot_sessions FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_sessions_team_select ON agent.bot_sessions FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_sessions_team_update ON agent.bot_sessions FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE agent.mcp_connections ENABLE ROW LEVEL SECURITY;

CREATE POLICY mcp_connections_team_delete ON agent.mcp_connections FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY mcp_connections_team_insert ON agent.mcp_connections FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY mcp_connections_team_select ON agent.mcp_connections FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY mcp_connections_team_update ON agent.mcp_connections FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE agent.mcp_oauth_tokens ENABLE ROW LEVEL SECURITY;

CREATE POLICY mcp_oauth_tokens_team_delete ON agent.mcp_oauth_tokens FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY mcp_oauth_tokens_team_insert ON agent.mcp_oauth_tokens FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY mcp_oauth_tokens_team_select ON agent.mcp_oauth_tokens FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY mcp_oauth_tokens_team_update ON agent.mcp_oauth_tokens FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE agent.schedule ENABLE ROW LEVEL SECURITY;

ALTER TABLE agent.schedule_logs ENABLE ROW LEVEL SECURITY;

CREATE POLICY schedule_logs_team_delete ON agent.schedule_logs FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY schedule_logs_team_insert ON agent.schedule_logs FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY schedule_logs_team_select ON agent.schedule_logs FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY schedule_logs_team_update ON agent.schedule_logs FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY schedule_team_delete ON agent.schedule FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY schedule_team_insert ON agent.schedule FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY schedule_team_select ON agent.schedule FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY schedule_team_update ON agent.schedule FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE agent.subagent_configs ENABLE ROW LEVEL SECURITY;

CREATE POLICY subagent_configs_team_delete ON agent.subagent_configs FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY subagent_configs_team_insert ON agent.subagent_configs FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY subagent_configs_team_select ON agent.subagent_configs FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY subagent_configs_team_update ON agent.subagent_configs FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE agent.tool_approval_requests ENABLE ROW LEVEL SECURITY;

CREATE POLICY tool_approval_requests_team_delete ON agent.tool_approval_requests FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY tool_approval_requests_team_insert ON agent.tool_approval_requests FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY tool_approval_requests_team_select ON agent.tool_approval_requests FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY tool_approval_requests_team_update ON agent.tool_approval_requests FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE agent.user_input_requests ENABLE ROW LEVEL SECURITY;

CREATE POLICY user_input_requests_team_delete ON agent.user_input_requests FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY user_input_requests_team_insert ON agent.user_input_requests FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY user_input_requests_team_select ON agent.user_input_requests FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY user_input_requests_team_update ON agent.user_input_requests FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

-- Explicit privilege contract for agent (business role has DML only; no DDL/BYPASSRLS).
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA agent TO memoh_agent;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA agent TO memoh_agent;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA agent TO memoh_agent;
DO $version_priv$
BEGIN
  IF to_regclass('agent.goose_db_version') IS NOT NULL THEN
    ALTER TABLE agent.goose_db_version OWNER TO memoh_migrate;
    REVOKE ALL ON TABLE agent.goose_db_version FROM PUBLIC;
    GRANT SELECT ON TABLE agent.goose_db_version TO memoh_agent;
    REVOKE INSERT, UPDATE, DELETE ON TABLE agent.goose_db_version FROM memoh_agent;
  END IF;
END
$version_priv$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Destructive epoch v2 baseline rollback is unsupported.
SELECT 'epoch_v2_baseline_down_noop'::text;
-- +goose StatementEnd
