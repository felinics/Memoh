-- +goose Up
-- +goose StatementBegin
-- Goose SessionLock may SET ROLE memoh_migrate before this script runs.
-- Reset to the deploying login so this owner can normalize its schema and ledger.
RESET ROLE;

CREATE SCHEMA IF NOT EXISTS memory;
ALTER SCHEMA memory OWNER TO memoh_migrate;

DO $defaults_login$
DECLARE
  login_role text := session_user;
BEGIN
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA memory GRANT ALL ON TABLES TO memoh_migrate', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA memory GRANT ALL ON SEQUENCES TO memoh_migrate', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA memory GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO memoh_memory', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA memory GRANT USAGE, SELECT ON SEQUENCES TO memoh_memory', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA memory GRANT EXECUTE ON FUNCTIONS TO memoh_memory', login_role);
END
$defaults_login$;

DO $version_owner$
BEGIN
  IF to_regclass('memory.goose_db_version') IS NOT NULL THEN
    ALTER TABLE memory.goose_db_version OWNER TO memoh_migrate;
  END IF;
END
$version_owner$;

SET LOCAL ROLE memoh_migrate;

REVOKE ALL ON SCHEMA memory FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA memory TO memoh_migrate;
GRANT USAGE ON SCHEMA memory TO memoh_memory;

ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA memory GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO memoh_memory;
ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA memory GRANT USAGE, SELECT ON SEQUENCES TO memoh_memory;
ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA memory GRANT EXECUTE ON FUNCTIONS TO memoh_memory;

CREATE TABLE memory.memory_edges (
    id bigint NOT NULL,
    bot_id uuid NOT NULL,
    src_node text NOT NULL,
    dst_node text NOT NULL,
    rel text NOT NULL,
    weight real DEFAULT 1.0 NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY memory.memory_edges FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE memory.memory_edges_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE memory.memory_edges_id_seq OWNED BY memory.memory_edges.id;

CREATE TABLE memory.memory_nodes (
    id text NOT NULL,
    bot_id uuid NOT NULL,
    body text NOT NULL,
    hash text NOT NULL,
    layer text DEFAULT 'note'::text NOT NULL,
    fact_type text DEFAULT ''::text NOT NULL,
    subject text DEFAULT ''::text NOT NULL,
    confidence real DEFAULT 0.5 NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    source_message_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    profile_ref text DEFAULT ''::text NOT NULL,
    topic text DEFAULT ''::text NOT NULL,
    captured_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT memory_nodes_confidence_check CHECK (((confidence >= (0)::double precision) AND (confidence <= (1)::double precision)))
);

ALTER TABLE ONLY memory.memory_nodes FORCE ROW LEVEL SECURITY;

CREATE TABLE memory.memory_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY memory.memory_providers FORCE ROW LEVEL SECURITY;

ALTER TABLE ONLY memory.memory_edges ALTER COLUMN id SET DEFAULT nextval('memory.memory_edges_id_seq'::regclass);

ALTER TABLE ONLY memory.memory_nodes
    ADD CONSTRAINT memoh_team_key_354c9a71ade3 UNIQUE (team_id, id);

ALTER TABLE ONLY memory.memory_edges
    ADD CONSTRAINT memoh_team_key_483f14eea5aa UNIQUE (team_id, id);

ALTER TABLE ONLY memory.memory_providers
    ADD CONSTRAINT memoh_team_key_8babb2fd7a49 UNIQUE (team_id, id);

ALTER TABLE ONLY memory.memory_edges
    ADD CONSTRAINT memory_edges_pkey PRIMARY KEY (id);

ALTER TABLE ONLY memory.memory_edges
    ADD CONSTRAINT memory_edges_unique UNIQUE (team_id, bot_id, src_node, dst_node, rel);

ALTER TABLE ONLY memory.memory_nodes
    ADD CONSTRAINT memory_nodes_pkey PRIMARY KEY (id);

ALTER TABLE ONLY memory.memory_providers
    ADD CONSTRAINT memory_providers_name_unique UNIQUE (team_id, name);

ALTER TABLE ONLY memory.memory_providers
    ADD CONSTRAINT memory_providers_pkey PRIMARY KEY (id);

CREATE INDEX idx_memory_edges_dst ON memory.memory_edges USING btree (team_id, bot_id, dst_node);

CREATE INDEX idx_memory_edges_rel ON memory.memory_edges USING btree (team_id, bot_id, rel);

CREATE INDEX idx_memory_edges_src ON memory.memory_edges USING btree (team_id, bot_id, src_node);

CREATE INDEX idx_memory_nodes_bot_layer ON memory.memory_nodes USING btree (team_id, bot_id, layer);

CREATE INDEX idx_memory_nodes_bot_prof ON memory.memory_nodes USING btree (team_id, bot_id, profile_ref);

CREATE INDEX idx_memory_nodes_bot_topic ON memory.memory_nodes USING btree (team_id, bot_id, topic);

CREATE INDEX idx_memory_nodes_updated ON memory.memory_nodes USING btree (team_id, bot_id, updated_at DESC);

ALTER TABLE memory.memory_edges ENABLE ROW LEVEL SECURITY;

CREATE POLICY memory_edges_team_delete ON memory.memory_edges FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY memory_edges_team_insert ON memory.memory_edges FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY memory_edges_team_select ON memory.memory_edges FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY memory_edges_team_update ON memory.memory_edges FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE memory.memory_nodes ENABLE ROW LEVEL SECURITY;

CREATE POLICY memory_nodes_team_delete ON memory.memory_nodes FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY memory_nodes_team_insert ON memory.memory_nodes FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY memory_nodes_team_select ON memory.memory_nodes FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY memory_nodes_team_update ON memory.memory_nodes FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE memory.memory_providers ENABLE ROW LEVEL SECURITY;

CREATE POLICY memory_providers_team_delete ON memory.memory_providers FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY memory_providers_team_insert ON memory.memory_providers FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY memory_providers_team_select ON memory.memory_providers FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY memory_providers_team_update ON memory.memory_providers FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

-- Explicit privilege contract for memory (business role has DML only; no DDL/BYPASSRLS).
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA memory TO memoh_memory;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA memory TO memoh_memory;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA memory TO memoh_memory;
DO $version_priv$
BEGIN
  IF to_regclass('memory.goose_db_version') IS NOT NULL THEN
    ALTER TABLE memory.goose_db_version OWNER TO memoh_migrate;
    REVOKE ALL ON TABLE memory.goose_db_version FROM PUBLIC;
    GRANT SELECT ON TABLE memory.goose_db_version TO memoh_memory;
    REVOKE INSERT, UPDATE, DELETE ON TABLE memory.goose_db_version FROM memoh_memory;
  END IF;
END
$version_priv$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Destructive epoch v2 baseline rollback is unsupported.
SELECT 'epoch_v2_baseline_down_noop'::text;
-- +goose StatementEnd
