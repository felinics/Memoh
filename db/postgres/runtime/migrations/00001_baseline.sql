-- +goose Up
-- +goose StatementBegin
-- Goose SessionLock may SET ROLE memoh_migrate before this script runs.
-- Reset to the deploying login so this owner can normalize its schema and ledger.
RESET ROLE;

CREATE SCHEMA IF NOT EXISTS runtime;
ALTER SCHEMA runtime OWNER TO memoh_migrate;

DO $defaults_login$
DECLARE
  login_role text := session_user;
BEGIN
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA runtime GRANT ALL ON TABLES TO memoh_migrate', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA runtime GRANT ALL ON SEQUENCES TO memoh_migrate', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA runtime GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO memoh_runtime', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA runtime GRANT USAGE, SELECT ON SEQUENCES TO memoh_runtime', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA runtime GRANT EXECUTE ON FUNCTIONS TO memoh_runtime', login_role);
END
$defaults_login$;

DO $version_owner$
BEGIN
  IF to_regclass('runtime.goose_db_version') IS NOT NULL THEN
    ALTER TABLE runtime.goose_db_version OWNER TO memoh_migrate;
  END IF;
END
$version_owner$;

SET LOCAL ROLE memoh_migrate;

REVOKE ALL ON SCHEMA runtime FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA runtime TO memoh_migrate;
GRANT USAGE ON SCHEMA runtime TO memoh_runtime;

ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA runtime GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO memoh_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA runtime GRANT USAGE, SELECT ON SEQUENCES TO memoh_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA runtime GRANT EXECUTE ON FUNCTIONS TO memoh_runtime;

CREATE TABLE runtime.bot_remote_runtime_bindings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    runtime_id uuid NOT NULL,
    is_primary boolean DEFAULT false NOT NULL,
    tool_approval_config jsonb DEFAULT '{"exec": {"mode": "ask", "bypass_commands": [], "force_review_commands": []}, "read": {"mode": "allow", "bypass_globs": [], "force_review_globs": []}, "write": {"mode": "ask", "bypass_globs": [], "force_review_globs": []}, "enabled": true}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY runtime.bot_remote_runtime_bindings FORCE ROW LEVEL SECURITY;

CREATE TABLE runtime.bot_workspace_resource_limits (
    bot_id uuid NOT NULL,
    cpu_millicores bigint DEFAULT 0 NOT NULL,
    memory_bytes bigint DEFAULT 0 NOT NULL,
    storage_bytes bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT bot_workspace_resource_limits_cpu_check CHECK ((cpu_millicores >= 0)),
    CONSTRAINT bot_workspace_resource_limits_memory_check CHECK ((memory_bytes >= 0)),
    CONSTRAINT bot_workspace_resource_limits_storage_check CHECK ((storage_bytes >= 0))
);

ALTER TABLE ONLY runtime.bot_workspace_resource_limits FORCE ROW LEVEL SECURITY;

CREATE TABLE runtime.container_versions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    container_id text NOT NULL,
    snapshot_id uuid NOT NULL,
    version integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY runtime.container_versions FORCE ROW LEVEL SECURITY;

CREATE TABLE runtime.containers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    container_id text NOT NULL,
    container_name text NOT NULL,
    image text NOT NULL,
    status text DEFAULT 'created'::text NOT NULL,
    namespace text DEFAULT 'default'::text NOT NULL,
    auto_start boolean DEFAULT true NOT NULL,
    container_path text DEFAULT '/data'::text NOT NULL,
    workspace_backend text DEFAULT 'container'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    last_started_at timestamp with time zone,
    last_stopped_at timestamp with time zone,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY runtime.containers FORCE ROW LEVEL SECURITY;

CREATE TABLE runtime.lifecycle_events (
    id text NOT NULL,
    container_id text NOT NULL,
    event_type text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY runtime.lifecycle_events FORCE ROW LEVEL SECURITY;

CREATE TABLE runtime.snapshots (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    container_id text NOT NULL,
    runtime_snapshot_name text NOT NULL,
    display_name text,
    parent_runtime_snapshot_name text,
    snapshotter text NOT NULL,
    source text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY runtime.snapshots FORCE ROW LEVEL SECURITY;

CREATE TABLE runtime.tasks (
    id character varying(255) NOT NULL,
    bot_id character varying(255) NOT NULL,
    name character varying(255) NOT NULL,
    command text NOT NULL,
    status character varying(50) DEFAULT 'pending'::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    exec_id character varying(255),
    pid integer,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY runtime.tasks FORCE ROW LEVEL SECURITY;

CREATE TABLE runtime.user_runtimes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    name text NOT NULL,
    api_token text NOT NULL,
    revoked_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT user_runtimes_name_check CHECK ((btrim(name) <> ''::text))
);

ALTER TABLE ONLY runtime.user_runtimes FORCE ROW LEVEL SECURITY;

ALTER TABLE ONLY runtime.bot_remote_runtime_bindings
    ADD CONSTRAINT bot_remote_runtime_bindings_bot_id_runtime_id_key UNIQUE (team_id, bot_id, runtime_id);

ALTER TABLE ONLY runtime.bot_remote_runtime_bindings
    ADD CONSTRAINT bot_remote_runtime_bindings_pkey PRIMARY KEY (id);

ALTER TABLE ONLY runtime.bot_workspace_resource_limits
    ADD CONSTRAINT bot_workspace_resource_limits_pkey PRIMARY KEY (bot_id);

ALTER TABLE ONLY runtime.container_versions
    ADD CONSTRAINT container_versions_container_id_version_key UNIQUE (team_id, container_id, version);

ALTER TABLE ONLY runtime.container_versions
    ADD CONSTRAINT container_versions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY runtime.containers
    ADD CONSTRAINT containers_container_id_unique UNIQUE (team_id, container_id);

ALTER TABLE ONLY runtime.containers
    ADD CONSTRAINT containers_container_name_unique UNIQUE (team_id, container_name);

ALTER TABLE ONLY runtime.containers
    ADD CONSTRAINT containers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY runtime.lifecycle_events
    ADD CONSTRAINT lifecycle_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY runtime.bot_workspace_resource_limits
    ADD CONSTRAINT memoh_team_key_1ea7647190ff UNIQUE (team_id, bot_id);

ALTER TABLE ONLY runtime.lifecycle_events
    ADD CONSTRAINT memoh_team_key_38bce59cf848 UNIQUE (team_id, id);

ALTER TABLE ONLY runtime.container_versions
    ADD CONSTRAINT memoh_team_key_4e4a7126f607 UNIQUE (team_id, id);

ALTER TABLE ONLY runtime.snapshots
    ADD CONSTRAINT memoh_team_key_6bceec3b7fb5 UNIQUE (team_id, id);

ALTER TABLE ONLY runtime.containers
    ADD CONSTRAINT memoh_team_key_a179a87cbe03 UNIQUE (team_id, id);

ALTER TABLE ONLY runtime.tasks
    ADD CONSTRAINT memoh_team_key_ba45c4a36084 UNIQUE (team_id, id);

ALTER TABLE ONLY runtime.user_runtimes
    ADD CONSTRAINT memoh_team_key_eeb71d700aa6 UNIQUE (team_id, id);

ALTER TABLE ONLY runtime.bot_remote_runtime_bindings
    ADD CONSTRAINT memoh_team_key_fc924c120c12 UNIQUE (team_id, id);

ALTER TABLE ONLY runtime.snapshots
    ADD CONSTRAINT snapshots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY runtime.tasks
    ADD CONSTRAINT tasks_pkey PRIMARY KEY (id);

ALTER TABLE ONLY runtime.user_runtimes
    ADD CONSTRAINT user_runtimes_api_token_key UNIQUE (team_id, api_token);

ALTER TABLE ONLY runtime.user_runtimes
    ADD CONSTRAINT user_runtimes_pkey PRIMARY KEY (id);

CREATE INDEX idx_bot_remote_runtime_bindings_bot_id ON runtime.bot_remote_runtime_bindings USING btree (team_id, bot_id);

CREATE UNIQUE INDEX idx_bot_remote_runtime_bindings_primary ON runtime.bot_remote_runtime_bindings USING btree (bot_id) WHERE (is_primary = true);

CREATE INDEX idx_bot_remote_runtime_bindings_runtime_id ON runtime.bot_remote_runtime_bindings USING btree (team_id, runtime_id);

CREATE INDEX idx_container_versions_container_id ON runtime.container_versions USING btree (team_id, container_id);

CREATE INDEX idx_container_versions_snapshot_id ON runtime.container_versions USING btree (team_id, snapshot_id);

CREATE INDEX idx_containers_bot_id ON runtime.containers USING btree (team_id, bot_id);

CREATE INDEX idx_lifecycle_events_container_id ON runtime.lifecycle_events USING btree (team_id, container_id);

CREATE INDEX idx_lifecycle_events_event_type ON runtime.lifecycle_events USING btree (team_id, event_type);

CREATE INDEX idx_snapshots_container_created_at ON runtime.snapshots USING btree (team_id, container_id, created_at DESC);

CREATE UNIQUE INDEX idx_snapshots_container_runtime_name ON runtime.snapshots USING btree (team_id, container_id, runtime_snapshot_name);

CREATE INDEX idx_snapshots_runtime_name ON runtime.snapshots USING btree (team_id, runtime_snapshot_name);

CREATE INDEX idx_tasks_exec_id ON runtime.tasks USING btree (team_id, exec_id);

CREATE INDEX idx_tasks_pid ON runtime.tasks USING btree (team_id, pid);

CREATE UNIQUE INDEX idx_user_runtimes_active_user_name ON runtime.user_runtimes USING btree (user_id, lower(name)) WHERE (revoked_at IS NULL);

CREATE INDEX idx_user_runtimes_user_id ON runtime.user_runtimes USING btree (team_id, user_id);

ALTER TABLE ONLY runtime.bot_remote_runtime_bindings
    ADD CONSTRAINT bot_remote_runtime_bindings_runtime_id_fkey FOREIGN KEY (team_id, runtime_id) REFERENCES runtime.user_runtimes(team_id, id) ON DELETE RESTRICT;

ALTER TABLE ONLY runtime.container_versions
    ADD CONSTRAINT container_versions_container_id_fkey FOREIGN KEY (team_id, container_id) REFERENCES runtime.containers(team_id, container_id) ON DELETE CASCADE;

ALTER TABLE ONLY runtime.container_versions
    ADD CONSTRAINT container_versions_snapshot_id_fkey FOREIGN KEY (team_id, snapshot_id) REFERENCES runtime.snapshots(team_id, id) ON DELETE RESTRICT;

ALTER TABLE ONLY runtime.lifecycle_events
    ADD CONSTRAINT lifecycle_events_container_id_fkey FOREIGN KEY (team_id, container_id) REFERENCES runtime.containers(team_id, container_id) ON DELETE CASCADE;

ALTER TABLE ONLY runtime.snapshots
    ADD CONSTRAINT snapshots_container_id_fkey FOREIGN KEY (team_id, container_id) REFERENCES runtime.containers(team_id, container_id) ON DELETE CASCADE;

ALTER TABLE runtime.bot_remote_runtime_bindings ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_remote_runtime_bindings_team_delete ON runtime.bot_remote_runtime_bindings FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_remote_runtime_bindings_team_insert ON runtime.bot_remote_runtime_bindings FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_remote_runtime_bindings_team_select ON runtime.bot_remote_runtime_bindings FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_remote_runtime_bindings_team_update ON runtime.bot_remote_runtime_bindings FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE runtime.bot_workspace_resource_limits ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_workspace_resource_limits_team_delete ON runtime.bot_workspace_resource_limits FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_workspace_resource_limits_team_insert ON runtime.bot_workspace_resource_limits FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_workspace_resource_limits_team_select ON runtime.bot_workspace_resource_limits FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_workspace_resource_limits_team_update ON runtime.bot_workspace_resource_limits FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE runtime.container_versions ENABLE ROW LEVEL SECURITY;

CREATE POLICY container_versions_team_delete ON runtime.container_versions FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY container_versions_team_insert ON runtime.container_versions FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY container_versions_team_select ON runtime.container_versions FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY container_versions_team_update ON runtime.container_versions FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE runtime.containers ENABLE ROW LEVEL SECURITY;

CREATE POLICY containers_team_delete ON runtime.containers FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY containers_team_insert ON runtime.containers FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY containers_team_select ON runtime.containers FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY containers_team_update ON runtime.containers FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE runtime.lifecycle_events ENABLE ROW LEVEL SECURITY;

CREATE POLICY lifecycle_events_team_delete ON runtime.lifecycle_events FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY lifecycle_events_team_insert ON runtime.lifecycle_events FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY lifecycle_events_team_select ON runtime.lifecycle_events FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY lifecycle_events_team_update ON runtime.lifecycle_events FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE runtime.snapshots ENABLE ROW LEVEL SECURITY;

CREATE POLICY snapshots_team_delete ON runtime.snapshots FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY snapshots_team_insert ON runtime.snapshots FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY snapshots_team_select ON runtime.snapshots FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY snapshots_team_update ON runtime.snapshots FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE runtime.tasks ENABLE ROW LEVEL SECURITY;

CREATE POLICY tasks_team_delete ON runtime.tasks FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY tasks_team_insert ON runtime.tasks FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY tasks_team_select ON runtime.tasks FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY tasks_team_update ON runtime.tasks FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE runtime.user_runtimes ENABLE ROW LEVEL SECURITY;

CREATE POLICY user_runtimes_team_delete ON runtime.user_runtimes FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY user_runtimes_team_insert ON runtime.user_runtimes FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY user_runtimes_team_select ON runtime.user_runtimes FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY user_runtimes_team_update ON runtime.user_runtimes FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

-- Explicit privilege contract for runtime (business role has DML only; no DDL/BYPASSRLS).
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA runtime TO memoh_runtime;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA runtime TO memoh_runtime;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA runtime TO memoh_runtime;
DO $version_priv$
BEGIN
  IF to_regclass('runtime.goose_db_version') IS NOT NULL THEN
    ALTER TABLE runtime.goose_db_version OWNER TO memoh_migrate;
    REVOKE ALL ON TABLE runtime.goose_db_version FROM PUBLIC;
    GRANT SELECT ON TABLE runtime.goose_db_version TO memoh_runtime;
    REVOKE INSERT, UPDATE, DELETE ON TABLE runtime.goose_db_version FROM memoh_runtime;
  END IF;
END
$version_priv$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Destructive epoch v2 baseline rollback is unsupported.
SELECT 'epoch_v2_baseline_down_noop'::text;
-- +goose StatementEnd
