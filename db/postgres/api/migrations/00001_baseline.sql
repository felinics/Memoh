-- +goose Up
-- +goose StatementBegin
-- Goose SessionLock may SET ROLE memoh_migrate before this script runs.
-- Reset to the deploying login so this owner can normalize its schema and ledger.
RESET ROLE;

CREATE SCHEMA IF NOT EXISTS api;
ALTER SCHEMA api OWNER TO memoh_migrate;

DO $defaults_login$
DECLARE
  login_role text := session_user;
BEGIN
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA api GRANT ALL ON TABLES TO memoh_migrate', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA api GRANT ALL ON SEQUENCES TO memoh_migrate', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA api GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO memoh_api', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA api GRANT USAGE, SELECT ON SEQUENCES TO memoh_api', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA api GRANT EXECUTE ON FUNCTIONS TO memoh_api', login_role);
END
$defaults_login$;

DO $version_owner$
BEGIN
  IF to_regclass('api.goose_db_version') IS NOT NULL THEN
    ALTER TABLE api.goose_db_version OWNER TO memoh_migrate;
  END IF;
END
$version_owner$;

SET LOCAL ROLE memoh_migrate;

REVOKE ALL ON SCHEMA api FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA api TO memoh_migrate;
GRANT USAGE ON SCHEMA api TO memoh_api;

ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA api GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO memoh_api;
ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA api GRANT USAGE, SELECT ON SEQUENCES TO memoh_api;
ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA api GRANT EXECUTE ON FUNCTIONS TO memoh_api;

CREATE TABLE api.bot_acl_rules (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    action text NOT NULL,
    effect text NOT NULL,
    channel_identity_id uuid,
    source_channel text,
    source_conversation_type text,
    source_conversation_id text,
    source_thread_id text,
    created_by_user_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    description text,
    subject_channel_type text,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT bot_acl_rules_action_check CHECK ((action = 'chat.trigger'::text)),
    CONSTRAINT bot_acl_rules_effect_check CHECK ((effect = ANY (ARRAY['allow'::text, 'deny'::text]))),
    CONSTRAINT bot_acl_rules_source_conversation_type_check CHECK (((source_conversation_type IS NULL) OR (source_conversation_type = ANY (ARRAY['private'::text, 'group'::text, 'thread'::text])))),
    CONSTRAINT bot_acl_rules_source_scope_check CHECK ((((source_conversation_id IS NULL) AND (source_thread_id IS NULL)) OR (source_channel IS NOT NULL))),
    CONSTRAINT bot_acl_rules_source_thread_check CHECK (((source_thread_id IS NULL) OR (source_conversation_id IS NOT NULL))),
    CONSTRAINT bot_acl_rules_target_check CHECK (((subject_channel_type IS NULL) OR (btrim(subject_channel_type) <> ''::text)))
);

ALTER TABLE ONLY api.bot_acl_rules FORCE ROW LEVEL SECURITY;

CREATE TABLE api.bot_channel_admins (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    channel_identity_id uuid NOT NULL,
    granted boolean DEFAULT true NOT NULL,
    created_by_user_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY api.bot_channel_admins FORCE ROW LEVEL SECURITY;

CREATE TABLE api.bot_user_grants (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    subject_type text NOT NULL,
    user_id uuid,
    permissions jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_by_user_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT bot_user_grants_subject_type_check CHECK ((subject_type = ANY (ARRAY['user'::text, 'everyone'::text]))),
    CONSTRAINT bot_user_grants_subject_value_check CHECK ((((subject_type = 'user'::text) AND (user_id IS NOT NULL)) OR ((subject_type = 'everyone'::text) AND (user_id IS NULL))))
);

ALTER TABLE ONLY api.bot_user_grants FORCE ROW LEVEL SECURITY;

CREATE TABLE api.bots (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_user_id uuid NOT NULL,
    name text NOT NULL,
    display_name text,
    avatar_url text,
    timezone text,
    is_active boolean DEFAULT true NOT NULL,
    status text DEFAULT 'ready'::text NOT NULL,
    language text DEFAULT 'auto'::text NOT NULL,
    command_ui_language text DEFAULT 'auto'::text NOT NULL,
    reasoning_enabled boolean DEFAULT false NOT NULL,
    reasoning_effort text DEFAULT 'medium'::text NOT NULL,
    chat_model_id uuid,
    chat_runtime text DEFAULT 'model'::text NOT NULL,
    chat_acp_agent_id text,
    chat_acp_project_path text DEFAULT '/data'::text NOT NULL,
    chat_acp_project_mode text DEFAULT 'project'::text NOT NULL,
    search_provider_id uuid,
    fetch_provider_id uuid,
    memory_provider_id uuid,
    heartbeat_enabled boolean DEFAULT false NOT NULL,
    heartbeat_interval integer DEFAULT 1440 NOT NULL,
    heartbeat_prompt text DEFAULT ''::text NOT NULL,
    heartbeat_model_id uuid,
    compaction_enabled boolean DEFAULT false NOT NULL,
    compaction_threshold integer DEFAULT 100000 NOT NULL,
    compaction_ratio integer DEFAULT 80 NOT NULL,
    compaction_model_id uuid,
    image_model_id uuid,
    discuss_probe_model_id uuid,
    tts_model_id uuid,
    transcription_model_id uuid,
    video_model_id uuid,
    persist_full_tool_results boolean DEFAULT false NOT NULL,
    show_tool_calls_in_im boolean DEFAULT false NOT NULL,
    tool_approval_config jsonb DEFAULT '{"exec": {"bypass_commands": [], "require_approval": false, "force_review_commands": []}, "read": {"bypass_globs": [], "require_approval": false, "force_review_globs": []}, "write": {"bypass_globs": ["/data/**", "/tmp/**"], "require_approval": true, "force_review_globs": []}, "enabled": false}'::jsonb NOT NULL,
    display_enabled boolean DEFAULT false NOT NULL,
    overlay_provider text DEFAULT ''::text NOT NULL,
    overlay_enabled boolean DEFAULT false NOT NULL,
    overlay_config jsonb DEFAULT '{}'::jsonb NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    acl_default_effect text DEFAULT 'allow'::text NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT bots_acl_default_effect_check CHECK ((acl_default_effect = ANY (ARRAY['allow'::text, 'deny'::text]))),
    CONSTRAINT bots_chat_acp_project_mode_check CHECK ((chat_acp_project_mode = ANY (ARRAY['project'::text, 'none'::text]))),
    CONSTRAINT bots_chat_runtime_check CHECK ((chat_runtime = ANY (ARRAY['model'::text, 'acp_agent'::text]))),
    CONSTRAINT bots_name_format_check CHECK ((name ~ '^[a-z0-9][a-z0-9-]{1,62}$'::text)),
    CONSTRAINT bots_status_check CHECK ((status = ANY (ARRAY['creating'::text, 'ready'::text, 'deleting'::text])))
);

ALTER TABLE ONLY api.bots FORCE ROW LEVEL SECURITY;

CREATE TABLE api.channel_link_codes (
    token text NOT NULL,
    user_id uuid NOT NULL,
    channel_type text DEFAULT ''::text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    consumed_channel_identity_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY api.channel_link_codes FORCE ROW LEVEL SECURITY;

CREATE TABLE api.user_channel_bindings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    channel_type text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY api.user_channel_bindings FORCE ROW LEVEL SECURITY;

CREATE TABLE api.user_channel_identity_bindings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    channel_identity_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY api.user_channel_identity_bindings FORCE ROW LEVEL SECURITY;

ALTER TABLE ONLY api.bot_acl_rules
    ADD CONSTRAINT bot_acl_rules_pkey PRIMARY KEY (id);

ALTER TABLE ONLY api.bot_acl_rules
    ADD CONSTRAINT bot_acl_rules_unique_target UNIQUE NULLS NOT DISTINCT (team_id, bot_id, action, effect, channel_identity_id, subject_channel_type, source_conversation_type, source_conversation_id, source_thread_id);

ALTER TABLE ONLY api.bot_channel_admins
    ADD CONSTRAINT bot_channel_admins_pkey PRIMARY KEY (id);

ALTER TABLE ONLY api.bot_channel_admins
    ADD CONSTRAINT bot_channel_admins_unique UNIQUE (team_id, bot_id, channel_identity_id);

ALTER TABLE ONLY api.bot_user_grants
    ADD CONSTRAINT bot_user_grants_pkey PRIMARY KEY (id);

ALTER TABLE ONLY api.bots
    ADD CONSTRAINT bots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY api.channel_link_codes
    ADD CONSTRAINT channel_link_codes_pkey PRIMARY KEY (token);

ALTER TABLE ONLY api.user_channel_identity_bindings
    ADD CONSTRAINT memoh_team_key_013e28c14a2d UNIQUE (team_id, id);

ALTER TABLE ONLY api.bots
    ADD CONSTRAINT memoh_team_key_739502fe2ce0 UNIQUE (team_id, id);

ALTER TABLE ONLY api.bot_acl_rules
    ADD CONSTRAINT memoh_team_key_785971fcb634 UNIQUE (team_id, id);

ALTER TABLE ONLY api.bot_user_grants
    ADD CONSTRAINT memoh_team_key_a90f63175197 UNIQUE (team_id, id);

ALTER TABLE ONLY api.bot_channel_admins
    ADD CONSTRAINT memoh_team_key_bf4598b3f5ec UNIQUE (team_id, id);

ALTER TABLE ONLY api.user_channel_bindings
    ADD CONSTRAINT memoh_team_key_c58e4d82cd9a UNIQUE (team_id, id);

ALTER TABLE ONLY api.channel_link_codes
    ADD CONSTRAINT memoh_team_key_e76a99926790 UNIQUE (team_id, token);

ALTER TABLE ONLY api.user_channel_bindings
    ADD CONSTRAINT user_channel_bindings_pkey PRIMARY KEY (id);

ALTER TABLE ONLY api.user_channel_bindings
    ADD CONSTRAINT user_channel_bindings_unique UNIQUE (team_id, user_id, channel_type);

ALTER TABLE ONLY api.user_channel_identity_bindings
    ADD CONSTRAINT user_channel_identity_bindings_pkey PRIMARY KEY (id);

ALTER TABLE ONLY api.user_channel_identity_bindings
    ADD CONSTRAINT user_channel_identity_bindings_unique UNIQUE (team_id, user_id, channel_identity_id);

CREATE INDEX idx_bot_acl_rules_bot_id ON api.bot_acl_rules USING btree (team_id, bot_id);

CREATE INDEX idx_bot_acl_rules_channel_identity_id ON api.bot_acl_rules USING btree (team_id, channel_identity_id);

CREATE INDEX idx_bot_channel_admins_bot_id ON api.bot_channel_admins USING btree (team_id, bot_id);

CREATE INDEX idx_bot_channel_admins_channel_identity_id ON api.bot_channel_admins USING btree (team_id, channel_identity_id);

CREATE INDEX idx_bot_user_grants_bot_id ON api.bot_user_grants USING btree (team_id, bot_id);

CREATE UNIQUE INDEX idx_bot_user_grants_unique_everyone ON api.bot_user_grants USING btree (team_id, bot_id) WHERE (subject_type = 'everyone'::text);

CREATE UNIQUE INDEX idx_bot_user_grants_unique_user ON api.bot_user_grants USING btree (team_id, bot_id, user_id) WHERE (subject_type = 'user'::text);

CREATE INDEX idx_bot_user_grants_user_id ON api.bot_user_grants USING btree (team_id, user_id);

CREATE UNIQUE INDEX idx_bots_name ON api.bots USING btree (team_id, name);

CREATE INDEX idx_bots_owner_user_id ON api.bots USING btree (team_id, owner_user_id);

CREATE INDEX idx_channel_link_codes_user_id ON api.channel_link_codes USING btree (team_id, user_id);

CREATE INDEX idx_user_channel_bindings_user_id ON api.user_channel_bindings USING btree (team_id, user_id);

CREATE INDEX idx_user_channel_identity_bindings_channel_identity_id ON api.user_channel_identity_bindings USING btree (team_id, channel_identity_id);

CREATE INDEX idx_user_channel_identity_bindings_user_id ON api.user_channel_identity_bindings USING btree (team_id, user_id);

ALTER TABLE ONLY api.bot_acl_rules
    ADD CONSTRAINT bot_acl_rules_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES api.bots(team_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY api.bot_channel_admins
    ADD CONSTRAINT bot_channel_admins_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES api.bots(team_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY api.bot_user_grants
    ADD CONSTRAINT bot_user_grants_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES api.bots(team_id, id) ON DELETE CASCADE;

ALTER TABLE api.bot_acl_rules ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_acl_rules_team_delete ON api.bot_acl_rules FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_acl_rules_team_insert ON api.bot_acl_rules FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_acl_rules_team_select ON api.bot_acl_rules FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_acl_rules_team_update ON api.bot_acl_rules FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE api.bot_channel_admins ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_channel_admins_team_delete ON api.bot_channel_admins FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_channel_admins_team_insert ON api.bot_channel_admins FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_channel_admins_team_select ON api.bot_channel_admins FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_channel_admins_team_update ON api.bot_channel_admins FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE api.bot_user_grants ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_user_grants_team_delete ON api.bot_user_grants FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_user_grants_team_insert ON api.bot_user_grants FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_user_grants_team_select ON api.bot_user_grants FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_user_grants_team_update ON api.bot_user_grants FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE api.bots ENABLE ROW LEVEL SECURITY;

CREATE POLICY bots_team_delete ON api.bots FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bots_team_insert ON api.bots FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bots_team_select ON api.bots FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bots_team_update ON api.bots FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE api.channel_link_codes ENABLE ROW LEVEL SECURITY;

CREATE POLICY channel_link_codes_team_delete ON api.channel_link_codes FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY channel_link_codes_team_insert ON api.channel_link_codes FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY channel_link_codes_team_select ON api.channel_link_codes FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY channel_link_codes_team_update ON api.channel_link_codes FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE api.user_channel_bindings ENABLE ROW LEVEL SECURITY;

CREATE POLICY user_channel_bindings_team_delete ON api.user_channel_bindings FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY user_channel_bindings_team_insert ON api.user_channel_bindings FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY user_channel_bindings_team_select ON api.user_channel_bindings FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY user_channel_bindings_team_update ON api.user_channel_bindings FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE api.user_channel_identity_bindings ENABLE ROW LEVEL SECURITY;

CREATE POLICY user_channel_identity_bindings_team_delete ON api.user_channel_identity_bindings FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY user_channel_identity_bindings_team_insert ON api.user_channel_identity_bindings FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY user_channel_identity_bindings_team_select ON api.user_channel_identity_bindings FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY user_channel_identity_bindings_team_update ON api.user_channel_identity_bindings FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

-- Explicit privilege contract for api (business role has DML only; no DDL/BYPASSRLS).
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA api TO memoh_api;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA api TO memoh_api;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA api TO memoh_api;
DO $version_priv$
BEGIN
  IF to_regclass('api.goose_db_version') IS NOT NULL THEN
    ALTER TABLE api.goose_db_version OWNER TO memoh_migrate;
    REVOKE ALL ON TABLE api.goose_db_version FROM PUBLIC;
    GRANT SELECT ON TABLE api.goose_db_version TO memoh_api;
    REVOKE INSERT, UPDATE, DELETE ON TABLE api.goose_db_version FROM memoh_api;
  END IF;
END
$version_priv$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Destructive epoch v2 baseline rollback is unsupported.
SELECT 'epoch_v2_baseline_down_noop'::text;
-- +goose StatementEnd
