-- +goose Up
-- +goose StatementBegin
-- Goose SessionLock may SET ROLE memoh_migrate before this script runs.
-- Reset to the deploying login so this owner can normalize its schema and ledger.
RESET ROLE;

CREATE SCHEMA IF NOT EXISTS media;
ALTER SCHEMA media OWNER TO memoh_migrate;

DO $defaults_login$
DECLARE
  login_role text := session_user;
BEGIN
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA media GRANT ALL ON TABLES TO memoh_migrate', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA media GRANT ALL ON SEQUENCES TO memoh_migrate', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA media GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO memoh_media', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA media GRANT USAGE, SELECT ON SEQUENCES TO memoh_media', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA media GRANT EXECUTE ON FUNCTIONS TO memoh_media', login_role);
END
$defaults_login$;

DO $version_owner$
BEGIN
  IF to_regclass('media.goose_db_version') IS NOT NULL THEN
    ALTER TABLE media.goose_db_version OWNER TO memoh_migrate;
  END IF;
END
$version_owner$;

SET LOCAL ROLE memoh_migrate;

REVOKE ALL ON SCHEMA media FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA media TO memoh_migrate;
GRANT USAGE ON SCHEMA media TO memoh_media;

ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA media GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO memoh_media;
ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA media GRANT USAGE, SELECT ON SEQUENCES TO memoh_media;
ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA media GRANT EXECUTE ON FUNCTIONS TO memoh_media;

CREATE TABLE media.bot_storage_bindings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    storage_provider_id uuid NOT NULL,
    base_path text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY media.bot_storage_bindings FORCE ROW LEVEL SECURITY;

CREATE TABLE media.media_assets (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    storage_provider_id uuid,
    content_hash text NOT NULL,
    media_type text NOT NULL,
    mime text DEFAULT 'application/octet-stream'::text NOT NULL,
    size_bytes bigint DEFAULT 0 NOT NULL,
    storage_key text NOT NULL,
    original_name text,
    width integer,
    height integer,
    duration_ms bigint,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY media.media_assets FORCE ROW LEVEL SECURITY;

CREATE TABLE media.storage_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT storage_providers_provider_check CHECK ((provider = ANY (ARRAY['localfs'::text, 's3'::text, 'gcs'::text])))
);

ALTER TABLE ONLY media.storage_providers FORCE ROW LEVEL SECURITY;

ALTER TABLE ONLY media.bot_storage_bindings
    ADD CONSTRAINT bot_storage_bindings_pkey PRIMARY KEY (id);

ALTER TABLE ONLY media.bot_storage_bindings
    ADD CONSTRAINT bot_storage_bindings_unique UNIQUE (team_id, bot_id);

ALTER TABLE ONLY media.media_assets
    ADD CONSTRAINT media_assets_bot_hash_unique UNIQUE (team_id, bot_id, content_hash);

ALTER TABLE ONLY media.media_assets
    ADD CONSTRAINT media_assets_pkey PRIMARY KEY (id);

ALTER TABLE ONLY media.storage_providers
    ADD CONSTRAINT memoh_team_key_5d8907827b81 UNIQUE (team_id, id);

ALTER TABLE ONLY media.bot_storage_bindings
    ADD CONSTRAINT memoh_team_key_8c69ca54cae0 UNIQUE (team_id, id);

ALTER TABLE ONLY media.media_assets
    ADD CONSTRAINT memoh_team_key_a3b5e7229cdf UNIQUE (team_id, id);

ALTER TABLE ONLY media.storage_providers
    ADD CONSTRAINT storage_providers_name_unique UNIQUE (team_id, name);

ALTER TABLE ONLY media.storage_providers
    ADD CONSTRAINT storage_providers_pkey PRIMARY KEY (id);

CREATE INDEX idx_bot_storage_bindings_bot_id ON media.bot_storage_bindings USING btree (team_id, bot_id);

CREATE INDEX idx_media_assets_bot_id ON media.media_assets USING btree (team_id, bot_id);

CREATE INDEX idx_media_assets_content_hash ON media.media_assets USING btree (team_id, content_hash);

ALTER TABLE ONLY media.bot_storage_bindings
    ADD CONSTRAINT bot_storage_bindings_storage_provider_id_fkey FOREIGN KEY (team_id, storage_provider_id) REFERENCES media.storage_providers(team_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY media.media_assets
    ADD CONSTRAINT media_assets_storage_provider_id_fkey FOREIGN KEY (team_id, storage_provider_id) REFERENCES media.storage_providers(team_id, id) ON DELETE SET NULL (storage_provider_id);

ALTER TABLE media.bot_storage_bindings ENABLE ROW LEVEL SECURITY;

CREATE POLICY bot_storage_bindings_team_delete ON media.bot_storage_bindings FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_storage_bindings_team_insert ON media.bot_storage_bindings FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_storage_bindings_team_select ON media.bot_storage_bindings FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY bot_storage_bindings_team_update ON media.bot_storage_bindings FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE media.media_assets ENABLE ROW LEVEL SECURITY;

CREATE POLICY media_assets_team_delete ON media.media_assets FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY media_assets_team_insert ON media.media_assets FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY media_assets_team_select ON media.media_assets FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY media_assets_team_update ON media.media_assets FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE media.storage_providers ENABLE ROW LEVEL SECURITY;

CREATE POLICY storage_providers_team_delete ON media.storage_providers FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY storage_providers_team_insert ON media.storage_providers FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY storage_providers_team_select ON media.storage_providers FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY storage_providers_team_update ON media.storage_providers FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

-- Explicit privilege contract for media (business role has DML only; no DDL/BYPASSRLS).
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA media TO memoh_media;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA media TO memoh_media;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA media TO memoh_media;
DO $version_priv$
BEGIN
  IF to_regclass('media.goose_db_version') IS NOT NULL THEN
    ALTER TABLE media.goose_db_version OWNER TO memoh_migrate;
    REVOKE ALL ON TABLE media.goose_db_version FROM PUBLIC;
    GRANT SELECT ON TABLE media.goose_db_version TO memoh_media;
    REVOKE INSERT, UPDATE, DELETE ON TABLE media.goose_db_version FROM memoh_media;
  END IF;
END
$version_priv$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Destructive epoch v2 baseline rollback is unsupported.
SELECT 'epoch_v2_baseline_down_noop'::text;
-- +goose StatementEnd
