-- +goose Up
-- +goose StatementBegin
-- Epoch v2 IAM bootstrap: NOLOGIN group roles and IAM-owned schema objects.
-- Goose may pre-create <schema>.goose_db_version; this baseline never hand-creates it.
-- Runner login executes with SET ROLE memoh_migrate after membership grant (no superuser required).

CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;

DO $roles$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_migrate') THEN
    CREATE ROLE memoh_migrate NOLOGIN NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_iam') THEN
    CREATE ROLE memoh_iam NOLOGIN NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_api') THEN
    CREATE ROLE memoh_api NOLOGIN NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_agent') THEN
    CREATE ROLE memoh_agent NOLOGIN NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_channel') THEN
    CREATE ROLE memoh_channel NOLOGIN NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_memory') THEN
    CREATE ROLE memoh_memory NOLOGIN NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_runtime') THEN
    CREATE ROLE memoh_runtime NOLOGIN NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_model') THEN
    CREATE ROLE memoh_model NOLOGIN NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_media') THEN
    CREATE ROLE memoh_media NOLOGIN NOINHERIT;
  END IF;
END
$roles$;

-- Allow the deploying login to SET ROLE into migrate/owner groups without inheriting them.
DO $grant_login$
BEGIN
  EXECUTE format(
    'GRANT memoh_migrate, memoh_iam, memoh_api, memoh_agent, memoh_channel, memoh_memory, memoh_runtime, memoh_model, memoh_media TO %I',
    session_user
  );
EXCEPTION
  WHEN insufficient_privilege THEN
    IF NOT pg_has_role(session_user, 'memoh_migrate', 'member') THEN
      RAISE EXCEPTION
        'deploying login % lacks memoh_migrate membership and cannot GRANT it',
        session_user;
    END IF;
END
$grant_login$;

DO $search_path$
BEGIN
  ALTER ROLE memoh_iam SET search_path TO iam, public;
  ALTER ROLE memoh_api SET search_path TO api, iam, public;
  ALTER ROLE memoh_agent SET search_path TO agent, iam, public;
  ALTER ROLE memoh_channel SET search_path TO channel, iam, public;
  ALTER ROLE memoh_memory SET search_path TO memory, iam, public;
  ALTER ROLE memoh_runtime SET search_path TO runtime, iam, public;
  ALTER ROLE memoh_model SET search_path TO model, iam, public;
  ALTER ROLE memoh_media SET search_path TO media, iam, public;
  ALTER ROLE memoh_migrate SET search_path TO iam, public;
EXCEPTION
  WHEN insufficient_privilege THEN
    -- Roles may be cluster-precreated by a stronger admin; Migrator/admin must set search_path.
    RAISE NOTICE 'skipped ALTER ROLE search_path; deploying login lacks role admin rights';
END
$search_path$;

-- Runner (or Goose) may pre-create this schema; IAM owns only its schema contract.
CREATE SCHEMA IF NOT EXISTS iam;
ALTER SCHEMA iam OWNER TO memoh_migrate;

-- Default privileges for IAM objects created by the deploying login before SET ROLE.
DO $defaults_login$
DECLARE
  login_role text := session_user;
BEGIN
  -- Goose creates iam.goose_db_version as the deploying login before the
  -- migration body; memoh_migrate must still be able to list and stamp it.
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA iam GRANT ALL ON TABLES TO memoh_migrate', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA iam GRANT ALL ON SEQUENCES TO memoh_migrate', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA iam GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO memoh_iam', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA iam GRANT USAGE, SELECT ON SEQUENCES TO memoh_iam', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA iam GRANT EXECUTE ON FUNCTIONS TO memoh_iam', login_role);
END
$defaults_login$;

-- Goose pre-creates iam.goose_db_version as the deploying login; transfer
-- ownership before SET ROLE so later version inserts succeed as memoh_migrate.
DO $version_owner$
BEGIN
  IF to_regclass('iam.goose_db_version') IS NOT NULL THEN
    ALTER TABLE iam.goose_db_version OWNER TO memoh_migrate;
  END IF;
END
$version_owner$;

-- Subsequent object DDL in this baseline runs as memoh_migrate.
SET LOCAL ROLE memoh_migrate;

REVOKE ALL ON SCHEMA iam FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA iam TO memoh_migrate;
GRANT USAGE ON SCHEMA iam TO memoh_iam;
-- Business roles may resolve iam.memoh_current_team_id() and shared helpers.
GRANT USAGE ON SCHEMA iam TO memoh_api, memoh_agent, memoh_channel, memoh_memory, memoh_runtime, memoh_model, memoh_media;

-- Default privileges for IAM objects created while SET ROLE memoh_migrate.
ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA iam GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO memoh_iam;
ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA iam GRANT USAGE, SELECT ON SEQUENCES TO memoh_iam;
ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA iam GRANT EXECUTE ON FUNCTIONS TO memoh_iam;

CREATE TYPE iam.user_role AS ENUM (
    'member',
    'admin'
);

CREATE FUNCTION iam.memoh_current_team_id() RETURNS uuid
    LANGUAGE plpgsql STABLE
    SET search_path TO 'pg_catalog', 'pg_temp'
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

CREATE FUNCTION iam.memoh_guard_last_active_team_admin() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
BEGIN
    IF OLD.role <> 'admin'
       OR NOT OLD.is_active
       OR NOT EXISTS (
           SELECT 1
             FROM iam.users principal
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

    PERFORM 1
      FROM iam.teams
     WHERE id = OLD.team_id
     FOR UPDATE;

    IF NOT EXISTS (
        SELECT 1
          FROM iam.team_members candidate
          JOIN iam.users principal
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

CREATE TABLE iam.team_members (
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    user_id uuid NOT NULL,
    role iam.user_role DEFAULT 'member'::iam.user_role NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    title_model_id uuid,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY iam.team_members FORCE ROW LEVEL SECURITY;

CREATE TABLE iam.users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    username text,
    email text,
    password_hash text,
    display_name text,
    avatar_url text,
    timezone text DEFAULT 'UTC'::text NOT NULL,
    last_login_at timestamp with time zone,
    is_active boolean DEFAULT true NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE VIEW iam.team_accounts WITH (security_invoker='true') AS
 SELECT u.id,
    u.username,
    u.email,
    u.password_hash,
    tm.role,
    u.display_name,
    u.avatar_url,
    u.timezone,
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
   FROM (iam.team_members tm
     JOIN iam.users u ON ((u.id = tm.user_id)))
  WHERE (tm.team_id = iam.memoh_current_team_id());

CREATE TABLE iam.teams (
    id uuid NOT NULL,
    slug text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL
);

ALTER TABLE ONLY iam.teams FORCE ROW LEVEL SECURITY;

ALTER TABLE ONLY iam.team_members
    ADD CONSTRAINT team_members_pkey PRIMARY KEY (team_id, user_id);

ALTER TABLE ONLY iam.teams
    ADD CONSTRAINT teams_pkey PRIMARY KEY (id);

ALTER TABLE ONLY iam.users
    ADD CONSTRAINT users_email_unique UNIQUE (email);

ALTER TABLE ONLY iam.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

ALTER TABLE ONLY iam.users
    ADD CONSTRAINT users_username_unique UNIQUE (username);

CREATE INDEX team_members_user_id_idx ON iam.team_members USING btree (user_id);

CREATE UNIQUE INDEX teams_slug_unique ON iam.teams USING btree (slug) WHERE (slug IS NOT NULL);

CREATE TRIGGER team_members_last_active_admin_guard BEFORE DELETE OR UPDATE OF role, is_active ON iam.team_members FOR EACH ROW EXECUTE FUNCTION iam.memoh_guard_last_active_team_admin();

ALTER TABLE ONLY iam.team_members
    ADD CONSTRAINT team_members_team_id_fkey FOREIGN KEY (team_id) REFERENCES iam.teams(id) ON DELETE RESTRICT;

ALTER TABLE ONLY iam.team_members
    ADD CONSTRAINT team_members_user_id_fkey FOREIGN KEY (user_id) REFERENCES iam.users(id) ON DELETE CASCADE;

ALTER TABLE iam.team_members ENABLE ROW LEVEL SECURITY;

CREATE POLICY team_members_team_delete ON iam.team_members FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY team_members_team_insert ON iam.team_members FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY team_members_team_select ON iam.team_members FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY team_members_team_update ON iam.team_members FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE iam.teams ENABLE ROW LEVEL SECURITY;

CREATE POLICY teams_self_select ON iam.teams FOR SELECT USING ((id = iam.memoh_current_team_id()));

-- Explicit privilege contract for iam (business role has DML only; no DDL/BYPASSRLS).
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA iam TO memoh_iam;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA iam TO memoh_iam;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA iam TO memoh_iam;
GRANT EXECUTE ON FUNCTION iam.memoh_current_team_id() TO memoh_iam, memoh_api, memoh_agent, memoh_channel, memoh_memory, memoh_runtime, memoh_model, memoh_media, memoh_migrate;
GRANT EXECUTE ON FUNCTION iam.memoh_guard_last_active_team_admin() TO memoh_iam, memoh_migrate;
DO $version_priv$
BEGIN
  IF to_regclass('iam.goose_db_version') IS NOT NULL THEN
    ALTER TABLE iam.goose_db_version OWNER TO memoh_migrate;
    REVOKE ALL ON TABLE iam.goose_db_version FROM PUBLIC;
    GRANT SELECT ON TABLE iam.goose_db_version TO memoh_iam;
    REVOKE INSERT, UPDATE, DELETE ON TABLE iam.goose_db_version FROM memoh_iam;
  END IF;
END
$version_priv$;

-- Default singleton team required for fresh installs (schema-only dump has no data).
-- teams uses FORCE RLS with select-only policies; briefly relax force for the seed row.
ALTER TABLE iam.teams NO FORCE ROW LEVEL SECURITY;
INSERT INTO iam.teams (id, slug, created_at, updated_at, metadata)
VALUES (
  '00000000-0000-0000-0000-000000000001',
  'default',
  now(),
  now(),
  '{}'::jsonb
)
ON CONFLICT (id) DO NOTHING;
ALTER TABLE iam.teams FORCE ROW LEVEL SECURITY;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Destructive epoch v2 baseline rollback is unsupported.
SELECT 'epoch_v2_baseline_down_noop'::text;
-- +goose StatementEnd
