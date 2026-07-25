-- +goose Up
-- +goose StatementBegin
-- Goose SessionLock may SET ROLE memoh_migrate before this script runs.
-- Reset to the deploying login so this owner can normalize its schema and ledger.
RESET ROLE;

CREATE SCHEMA IF NOT EXISTS channel;
ALTER SCHEMA channel OWNER TO memoh_migrate;

DO $defaults_login$
DECLARE
  login_role text := session_user;
BEGIN
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA channel GRANT ALL ON TABLES TO memoh_migrate', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA channel GRANT ALL ON SEQUENCES TO memoh_migrate', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA channel GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO memoh_channel', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA channel GRANT USAGE, SELECT ON SEQUENCES TO memoh_channel', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA channel GRANT EXECUTE ON FUNCTIONS TO memoh_channel', login_role);
END
$defaults_login$;

DO $version_owner$
BEGIN
  IF to_regclass('channel.goose_db_version') IS NOT NULL THEN
    ALTER TABLE channel.goose_db_version OWNER TO memoh_migrate;
  END IF;
END
$version_owner$;

SET LOCAL ROLE memoh_migrate;

REVOKE ALL ON SCHEMA channel FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA channel TO memoh_migrate;
GRANT USAGE ON SCHEMA channel TO memoh_channel;

ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA channel GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO memoh_channel;
ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA channel GRANT USAGE, SELECT ON SEQUENCES TO memoh_channel;
ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA channel GRANT EXECUTE ON FUNCTIONS TO memoh_channel;

CREATE TABLE channel.bot_channel_configs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    channel_type text NOT NULL,
    credentials jsonb DEFAULT '{}'::jsonb NOT NULL,
    external_identity text,
    self_identity jsonb DEFAULT '{}'::jsonb NOT NULL,
    routing jsonb DEFAULT '{}'::jsonb NOT NULL,
    capabilities jsonb DEFAULT '{}'::jsonb NOT NULL,
    disabled boolean DEFAULT false NOT NULL,
    verified_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY channel.bot_channel_configs FORCE ROW LEVEL SECURITY;

CREATE TABLE channel.bot_channel_routes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    channel_type text NOT NULL,
    channel_config_id uuid,
    external_conversation_id text NOT NULL,
    external_thread_id text,
    conversation_type text,
    default_reply_target text,
    active_session_id uuid,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY channel.bot_channel_routes FORCE ROW LEVEL SECURITY;

CREATE TABLE channel.bot_email_bindings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    email_provider_id uuid NOT NULL,
    email_address text NOT NULL,
    can_read boolean DEFAULT true NOT NULL,
    can_write boolean DEFAULT true NOT NULL,
    can_delete boolean DEFAULT false NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY channel.bot_email_bindings FORCE ROW LEVEL SECURITY;

CREATE TABLE channel.bot_session_discuss_cursors (
    session_id uuid NOT NULL,
    bot_id uuid NOT NULL,
    scope_key text DEFAULT 'default'::text NOT NULL,
    route_id uuid,
    source text DEFAULT ''::text NOT NULL,
    consumed_cursor bigint DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    consumed_event_cursor bigint DEFAULT 0 NOT NULL
);

ALTER TABLE ONLY channel.bot_session_discuss_cursors FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE channel.bot_session_event_cursor_seq
    AS bigint
    START WITH 1
    INCREMENT BY 1
    MINVALUE 1
    MAXVALUE 9007199254740991
    CACHE 1;

CREATE TABLE channel.bot_session_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    session_id uuid NOT NULL,
    event_kind text NOT NULL,
    event_data jsonb NOT NULL,
    external_message_id text,
    sender_channel_identity_id uuid,
    received_at_ms bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT bot_session_events_event_kind_check CHECK ((event_kind = ANY (ARRAY['message'::text, 'edit'::text, 'delete'::text, 'service'::text])))
);

ALTER TABLE ONLY channel.bot_session_events FORCE ROW LEVEL SECURITY;

CREATE TABLE channel.channel_identities (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    channel_type text NOT NULL,
    channel_subject_id text NOT NULL,
    display_name text,
    avatar_url text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY channel.channel_identities FORCE ROW LEVEL SECURITY;

CREATE TABLE channel.email_oauth_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email_provider_id uuid NOT NULL,
    email_address text DEFAULT ''::text NOT NULL,
    access_token text DEFAULT ''::text NOT NULL,
    refresh_token text DEFAULT ''::text NOT NULL,
    expires_at timestamp with time zone,
    scope text DEFAULT ''::text NOT NULL,
    state text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY channel.email_oauth_tokens FORCE ROW LEVEL SECURITY;

CREATE TABLE channel.email_outbox (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider_id uuid NOT NULL,
    bot_id uuid NOT NULL,
    message_id text DEFAULT ''::text NOT NULL,
    from_address text DEFAULT ''::text NOT NULL,
    to_addresses jsonb DEFAULT '[]'::jsonb NOT NULL,
    subject text DEFAULT ''::text NOT NULL,
    body_text text DEFAULT ''::text NOT NULL,
    body_html text DEFAULT ''::text NOT NULL,
    attachments jsonb DEFAULT '[]'::jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    sent_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT email_outbox_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'sent'::text, 'failed'::text])))
);

ALTER TABLE ONLY channel.email_outbox FORCE ROW LEVEL SECURITY;

CREATE TABLE channel.email_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY channel.email_providers FORCE ROW LEVEL SECURITY;

ALTER TABLE ONLY channel.bot_channel_configs
    ADD CONSTRAINT bot_channel_configs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY channel.bot_channel_routes
    ADD CONSTRAINT bot_channel_routes_pkey PRIMARY KEY (id);

ALTER TABLE ONLY channel.bot_channel_configs
    ADD CONSTRAINT bot_channel_unique UNIQUE (team_id, bot_id, channel_type);

ALTER TABLE ONLY channel.bot_email_bindings
    ADD CONSTRAINT bot_email_bindings_pkey PRIMARY KEY (id);

ALTER TABLE ONLY channel.bot_email_bindings
    ADD CONSTRAINT bot_email_bindings_unique UNIQUE (team_id, bot_id, email_provider_id);

ALTER TABLE ONLY channel.bot_session_discuss_cursors
    ADD CONSTRAINT bot_session_discuss_cursors_pkey PRIMARY KEY (session_id, scope_key);

ALTER TABLE ONLY channel.bot_session_events
    ADD CONSTRAINT bot_session_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY channel.channel_identities
    ADD CONSTRAINT channel_identities_channel_type_subject_unique UNIQUE (team_id, channel_type, channel_subject_id);

ALTER TABLE ONLY channel.channel_identities
    ADD CONSTRAINT channel_identities_pkey PRIMARY KEY (id);

ALTER TABLE ONLY channel.email_oauth_tokens
    ADD CONSTRAINT email_oauth_tokens_email_provider_id_key UNIQUE (team_id, email_provider_id);

ALTER TABLE ONLY channel.email_oauth_tokens
    ADD CONSTRAINT email_oauth_tokens_pkey PRIMARY KEY (id);

ALTER TABLE ONLY channel.email_outbox
    ADD CONSTRAINT email_outbox_pkey PRIMARY KEY (id);

ALTER TABLE ONLY channel.email_providers
    ADD CONSTRAINT email_providers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY channel.email_providers
    ADD CONSTRAINT email_providers_user_name_unique UNIQUE (team_id, user_id, name);

ALTER TABLE ONLY channel.email_oauth_tokens
    ADD CONSTRAINT memoh_team_key_0b525bc25d91 UNIQUE (team_id, id);

ALTER TABLE ONLY channel.email_providers
    ADD CONSTRAINT memoh_team_key_41adeda78ae7 UNIQUE (team_id, id);

ALTER TABLE ONLY channel.bot_session_discuss_cursors
    ADD CONSTRAINT memoh_team_key_48c0ed6bd276 UNIQUE (team_id, session_id, scope_key);

ALTER TABLE ONLY channel.bot_email_bindings
    ADD CONSTRAINT memoh_team_key_812bc98005b4 UNIQUE (team_id, id);

ALTER TABLE ONLY channel.bot_session_events
    ADD CONSTRAINT memoh_team_key_9bea1a79597b UNIQUE (team_id, id);

ALTER TABLE ONLY channel.bot_channel_routes
    ADD CONSTRAINT memoh_team_key_b01c434242c4 UNIQUE (team_id, id);

ALTER TABLE ONLY channel.bot_channel_configs
    ADD CONSTRAINT memoh_team_key_b070e46de89b UNIQUE (team_id, id);

ALTER TABLE ONLY channel.email_outbox
    ADD CONSTRAINT memoh_team_key_b6fd1bd87341 UNIQUE (team_id, id);

ALTER TABLE ONLY channel.channel_identities
    ADD CONSTRAINT memoh_team_key_f6a93be346d8 UNIQUE (team_id, id);

CREATE INDEX idx_bot_channel_bot_id ON channel.bot_channel_configs USING btree (team_id, bot_id);

CREATE UNIQUE INDEX idx_bot_channel_external_identity ON channel.bot_channel_configs USING btree (team_id, channel_type, external_identity);

CREATE INDEX idx_bot_channel_routes_bot ON channel.bot_channel_routes USING btree (team_id, bot_id);

CREATE UNIQUE INDEX idx_bot_channel_routes_unique ON channel.bot_channel_routes USING btree (team_id, bot_id, channel_type, external_conversation_id, COALESCE(external_thread_id, ''::text));

CREATE INDEX idx_bot_email_bindings_bot_id ON channel.bot_email_bindings USING btree (team_id, bot_id);

CREATE INDEX idx_bot_email_bindings_provider_id ON channel.bot_email_bindings USING btree (team_id, email_provider_id);

CREATE INDEX idx_bot_session_discuss_cursors_bot ON channel.bot_session_discuss_cursors USING btree (team_id, bot_id);

CREATE INDEX idx_bot_session_discuss_cursors_route ON channel.bot_session_discuss_cursors USING btree (team_id, route_id) WHERE (route_id IS NOT NULL);

CREATE INDEX idx_email_oauth_tokens_state ON channel.email_oauth_tokens USING btree (team_id, state) WHERE (state <> ''::text);

CREATE INDEX idx_email_outbox_bot_id ON channel.email_outbox USING btree (team_id, bot_id, created_at DESC);

CREATE INDEX idx_email_outbox_provider_id ON channel.email_outbox USING btree (team_id, provider_id);

CREATE INDEX idx_email_providers_user_id ON channel.email_providers USING btree (team_id, user_id);

CREATE UNIQUE INDEX idx_session_events_dedup ON channel.bot_session_events USING btree (team_id, session_id, event_kind, external_message_id) WHERE ((external_message_id IS NOT NULL) AND (external_message_id <> ''::text));

CREATE INDEX idx_session_events_session_received ON channel.bot_session_events USING btree (team_id, session_id, received_at_ms);

ALTER TABLE ONLY channel.bot_channel_routes
    ADD CONSTRAINT bot_channel_routes_channel_config_id_fkey FOREIGN KEY (team_id, channel_config_id) REFERENCES channel.bot_channel_configs(team_id, id) ON DELETE SET NULL (channel_config_id);

ALTER TABLE ONLY channel.bot_email_bindings
    ADD CONSTRAINT bot_email_bindings_email_provider_id_fkey FOREIGN KEY (team_id, email_provider_id) REFERENCES channel.email_providers(team_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY channel.bot_session_discuss_cursors
    ADD CONSTRAINT bot_session_discuss_cursors_route_id_fkey FOREIGN KEY (team_id, route_id) REFERENCES channel.bot_channel_routes(team_id, id) ON DELETE SET NULL (route_id);

ALTER TABLE ONLY channel.email_oauth_tokens
    ADD CONSTRAINT email_oauth_tokens_email_provider_id_fkey FOREIGN KEY (team_id, email_provider_id) REFERENCES channel.email_providers(team_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY channel.email_outbox
    ADD CONSTRAINT email_outbox_provider_id_fkey FOREIGN KEY (team_id, provider_id) REFERENCES channel.email_providers(team_id, id) ON DELETE CASCADE;

ALTER TABLE channel.bot_channel_configs ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_channel_configs_team_delete ON channel.bot_channel_configs FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_channel_configs_team_insert ON channel.bot_channel_configs FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_channel_configs_team_select ON channel.bot_channel_configs FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_channel_configs_team_update ON channel.bot_channel_configs FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE channel.bot_channel_routes ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_channel_routes_team_delete ON channel.bot_channel_routes FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_channel_routes_team_insert ON channel.bot_channel_routes FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_channel_routes_team_select ON channel.bot_channel_routes FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_channel_routes_team_update ON channel.bot_channel_routes FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE channel.bot_email_bindings ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_email_bindings_team_delete ON channel.bot_email_bindings FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_email_bindings_team_insert ON channel.bot_email_bindings FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_email_bindings_team_select ON channel.bot_email_bindings FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_email_bindings_team_update ON channel.bot_email_bindings FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE channel.bot_session_discuss_cursors ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_session_discuss_cursors_team_delete ON channel.bot_session_discuss_cursors FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_session_discuss_cursors_team_insert ON channel.bot_session_discuss_cursors FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_session_discuss_cursors_team_select ON channel.bot_session_discuss_cursors FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_session_discuss_cursors_team_update ON channel.bot_session_discuss_cursors FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE channel.bot_session_events ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_session_events_team_delete ON channel.bot_session_events FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_session_events_team_insert ON channel.bot_session_events FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_session_events_team_select ON channel.bot_session_events FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_session_events_team_update ON channel.bot_session_events FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE channel.channel_identities ENABLE ROW LEVEL SECURITY;

CREATE POLICY channel_identities_team_delete ON channel.channel_identities FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY channel_identities_team_insert ON channel.channel_identities FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY channel_identities_team_select ON channel.channel_identities FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY channel_identities_team_update ON channel.channel_identities FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE channel.email_oauth_tokens ENABLE ROW LEVEL SECURITY;

CREATE POLICY email_oauth_tokens_team_delete ON channel.email_oauth_tokens FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY email_oauth_tokens_team_insert ON channel.email_oauth_tokens FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY email_oauth_tokens_team_select ON channel.email_oauth_tokens FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY email_oauth_tokens_team_update ON channel.email_oauth_tokens FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE channel.email_outbox ENABLE ROW LEVEL SECURITY;

CREATE POLICY email_outbox_team_delete ON channel.email_outbox FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY email_outbox_team_insert ON channel.email_outbox FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY email_outbox_team_select ON channel.email_outbox FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY email_outbox_team_update ON channel.email_outbox FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE channel.email_providers ENABLE ROW LEVEL SECURITY;

CREATE POLICY email_providers_team_delete ON channel.email_providers FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY email_providers_team_insert ON channel.email_providers FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY email_providers_team_select ON channel.email_providers FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY email_providers_team_update ON channel.email_providers FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

-- Explicit privilege contract for channel (business role has DML only; no DDL/BYPASSRLS).
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA channel TO memoh_channel;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA channel TO memoh_channel;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA channel TO memoh_channel;
DO $version_priv$
BEGIN
  IF to_regclass('channel.goose_db_version') IS NOT NULL THEN
    ALTER TABLE channel.goose_db_version OWNER TO memoh_migrate;
    REVOKE ALL ON TABLE channel.goose_db_version FROM PUBLIC;
    GRANT SELECT ON TABLE channel.goose_db_version TO memoh_channel;
    REVOKE INSERT, UPDATE, DELETE ON TABLE channel.goose_db_version FROM memoh_channel;
  END IF;
END
$version_priv$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Destructive epoch v2 baseline rollback is unsupported.
SELECT 'epoch_v2_baseline_down_noop'::text;
-- +goose StatementEnd
