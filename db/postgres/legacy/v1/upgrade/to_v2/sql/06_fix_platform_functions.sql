-- v1 -> v2 bridge step 06: rewrite iam functions, normalize ownership to
-- memoh_migrate, and grant business-role DML contracts (fresh/upgrade parity).
-- plan.yaml step id is fix_platform_functions: this step normalizes
-- ownership and grants platform-wide, not just for iam.

CREATE OR REPLACE FUNCTION iam.memoh_current_team_id()
RETURNS uuid
LANGUAGE plpgsql
STABLE
SET search_path TO 'pg_catalog', 'pg_temp'
AS $function$
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
$function$;

CREATE OR REPLACE FUNCTION iam.memoh_guard_last_active_team_admin()
RETURNS trigger
LANGUAGE plpgsql
SET search_path TO 'pg_catalog', 'pg_temp'
AS $function$
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
$function$;

-- data_root is Runtime deployment configuration, not membership state. The
-- legacy column is intentionally discarded during the one-shot v2 upgrade.
DROP VIEW IF EXISTS iam.team_accounts;
ALTER TABLE iam.team_members DROP COLUMN IF EXISTS data_root;
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
   FROM iam.team_members tm
   JOIN iam.users u ON u.id = tm.user_id
  WHERE tm.team_id = iam.memoh_current_team_id();

-- Reassign ownership of all owner-schema business objects to memoh_migrate.
-- Uses format()/quote_ident via %I; never interpolates unchecked identifiers.
DO $reassign_owner$
DECLARE
  rel record;
  fn record;
  typ record;
  kind_sql text;
BEGIN
  FOR rel IN
    SELECT n.nspname AS schema_name, c.relname AS rel_name, c.relkind
      FROM pg_catalog.pg_class AS c
      JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
     WHERE n.nspname IN (
             'iam', 'api', 'agent', 'channel', 'memory', 'runtime', 'model', 'media'
           )
       AND c.relkind IN ('r', 'p', 'v', 'm', 'S')
       AND NOT (
         c.relname = 'goose_db_version' AND c.relkind = 'r'
       )
       -- Identity/serial sequences follow their table owner; ALTER SEQUENCE OWNER is unsupported for identity.
       AND NOT (
         c.relkind = 'S'
         AND EXISTS (
           SELECT 1
             FROM pg_catalog.pg_depend AS d
            WHERE d.classid = 'pg_catalog.pg_class'::pg_catalog.regclass
              AND d.objid = c.oid
              AND d.refclassid = 'pg_catalog.pg_class'::pg_catalog.regclass
              AND d.deptype IN ('a', 'i')
         )
       )
  LOOP
    kind_sql := CASE rel.relkind
      WHEN 'r' THEN 'TABLE'
      WHEN 'p' THEN 'TABLE'
      WHEN 'v' THEN 'VIEW'
      WHEN 'm' THEN 'MATERIALIZED VIEW'
      WHEN 'S' THEN 'SEQUENCE'
    END;
    EXECUTE format(
      'ALTER %s %I.%I OWNER TO memoh_migrate',
      kind_sql,
      rel.schema_name,
      rel.rel_name
    );
  END LOOP;

  -- Version tables (if Runner already stamped) also belong to memoh_migrate.
  FOR rel IN
    SELECT n.nspname AS schema_name
      FROM pg_catalog.pg_class AS c
      JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
     WHERE n.nspname IN (
             'iam', 'api', 'agent', 'channel', 'memory', 'runtime', 'model', 'media'
           )
       AND c.relname = 'goose_db_version'
       AND c.relkind = 'r'
  LOOP
    EXECUTE format(
      'ALTER TABLE %I.%I OWNER TO memoh_migrate',
      rel.schema_name,
      'goose_db_version'
    );
  END LOOP;

  FOR fn IN
    SELECT p.oid::pg_catalog.regprocedure AS sig
      FROM pg_catalog.pg_proc AS p
      JOIN pg_catalog.pg_namespace AS n ON n.oid = p.pronamespace
     WHERE n.nspname IN (
             'iam', 'api', 'agent', 'channel', 'memory', 'runtime', 'model', 'media'
           )
       AND p.prokind IN ('f', 'p', 'w')
  LOOP
    EXECUTE format('ALTER FUNCTION %s OWNER TO memoh_migrate', fn.sig);
  END LOOP;

  FOR typ IN
    SELECT n.nspname AS schema_name, t.typname AS type_name
      FROM pg_catalog.pg_type AS t
      JOIN pg_catalog.pg_namespace AS n ON n.oid = t.typnamespace
     WHERE n.nspname IN (
             'iam', 'api', 'agent', 'channel', 'memory', 'runtime', 'model', 'media'
           )
       AND t.typtype IN ('e', 'c', 'd')
       AND left(t.typname, 1) <> '_'
       -- Skip table/view row types (ownership follows the relation).
       AND NOT EXISTS (
         SELECT 1
           FROM pg_catalog.pg_class AS c
          WHERE c.reltype = t.oid
       )
  LOOP
    EXECUTE format(
      'ALTER TYPE %I.%I OWNER TO memoh_migrate',
      typ.schema_name,
      typ.type_name
    );
  END LOOP;

  IF to_regtype('iam.user_role') IS NOT NULL THEN
    ALTER TYPE iam.user_role OWNER TO memoh_migrate;
  END IF;
END
$reassign_owner$;

-- Grants require object-owner privileges; memoh_* roles are NOINHERIT.
SET LOCAL ROLE memoh_migrate;

-- Privilege contract: each business role gets DML only on its own schema.
DO $grant_business$
DECLARE
  rec record;
  peer text;
  peers text[] := ARRAY[
    'memoh_iam', 'memoh_api', 'memoh_agent', 'memoh_channel',
    'memoh_memory', 'memoh_runtime', 'memoh_model', 'memoh_media'
  ];
BEGIN
  FOR rec IN
    SELECT * FROM (VALUES
      ('iam', 'memoh_iam'),
      ('api', 'memoh_api'),
      ('agent', 'memoh_agent'),
      ('channel', 'memoh_channel'),
      ('memory', 'memoh_memory'),
      ('runtime', 'memoh_runtime'),
      ('model', 'memoh_model'),
      ('media', 'memoh_media')
    ) AS t(schema_name, role_name)
  LOOP
    EXECUTE format('REVOKE ALL ON SCHEMA %I FROM PUBLIC', rec.schema_name);
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', rec.schema_name, rec.role_name);
    EXECUTE format('REVOKE CREATE ON SCHEMA %I FROM %I', rec.schema_name, rec.role_name);

    -- Drop any accidental cross-owner table grants, then grant own-schema DML.
    FOREACH peer IN ARRAY peers LOOP
      EXECUTE format(
        'REVOKE ALL ON ALL TABLES IN SCHEMA %I FROM %I',
        rec.schema_name,
        peer
      );
      EXECUTE format(
        'REVOKE ALL ON ALL SEQUENCES IN SCHEMA %I FROM %I',
        rec.schema_name,
        peer
      );
      EXECUTE format(
        'REVOKE ALL ON ALL FUNCTIONS IN SCHEMA %I FROM %I',
        rec.schema_name,
        peer
      );
    END LOOP;

    EXECUTE format(
      'GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO %I',
      rec.schema_name,
      rec.role_name
    );
    EXECUTE format(
      'GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %I TO %I',
      rec.schema_name,
      rec.role_name
    );
    EXECUTE format(
      'GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA %I TO %I',
      rec.schema_name,
      rec.role_name
    );

    -- Version ledger is stamp-owned; business roles stay read-only when present.
    IF to_regclass(format('%I.%I', rec.schema_name, 'goose_db_version')) IS NOT NULL THEN
      EXECUTE format(
        'REVOKE INSERT, UPDATE, DELETE ON TABLE %I.%I FROM %I',
        rec.schema_name,
        'goose_db_version',
        rec.role_name
      );
      EXECUTE format(
        'GRANT SELECT ON TABLE %I.%I TO %I',
        rec.schema_name,
        'goose_db_version',
        rec.role_name
      );
    END IF;
  END LOOP;

  -- Shared iam helpers (explicit; overrides the ALL FUNCTIONS revoke/grant pass).
  GRANT USAGE ON SCHEMA iam TO memoh_iam, memoh_api, memoh_agent, memoh_channel,
    memoh_memory, memoh_runtime, memoh_model, memoh_media;
  GRANT EXECUTE ON FUNCTION iam.memoh_current_team_id() TO memoh_iam, memoh_api,
    memoh_agent, memoh_channel, memoh_memory, memoh_runtime, memoh_model, memoh_media, memoh_migrate;
  REVOKE EXECUTE ON FUNCTION iam.memoh_guard_last_active_team_admin() FROM PUBLIC;
  GRANT EXECUTE ON FUNCTION iam.memoh_guard_last_active_team_admin() TO memoh_iam, memoh_migrate;
END
$grant_business$;

RESET ROLE;

-- Legacy v1 ledger stays in public under the deploying login; never an owner object / no business grants.
DO $legacy_ledger$
BEGIN
  IF to_regclass('public.schema_migrations') IS NOT NULL THEN
    REVOKE ALL ON TABLE public.schema_migrations FROM memoh_iam, memoh_api, memoh_agent,
      memoh_channel, memoh_memory, memoh_runtime, memoh_model, memoh_media;
  END IF;
END
$legacy_ledger$;
