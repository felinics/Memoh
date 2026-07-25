-- v1 -> v2 bridge: bootstrap NOLOGIN group roles and owner schemas.
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
    RAISE NOTICE 'skipped ALTER ROLE search_path; deploying login lacks role admin rights';
END
$search_path$;

CREATE SCHEMA IF NOT EXISTS iam;
CREATE SCHEMA IF NOT EXISTS api;
CREATE SCHEMA IF NOT EXISTS agent;
CREATE SCHEMA IF NOT EXISTS channel;
CREATE SCHEMA IF NOT EXISTS memory;
CREATE SCHEMA IF NOT EXISTS runtime;
CREATE SCHEMA IF NOT EXISTS model;
CREATE SCHEMA IF NOT EXISTS media;

ALTER SCHEMA iam OWNER TO memoh_migrate;
ALTER SCHEMA api OWNER TO memoh_migrate;
ALTER SCHEMA agent OWNER TO memoh_migrate;
ALTER SCHEMA channel OWNER TO memoh_migrate;
ALTER SCHEMA memory OWNER TO memoh_migrate;
ALTER SCHEMA runtime OWNER TO memoh_migrate;
ALTER SCHEMA model OWNER TO memoh_migrate;
ALTER SCHEMA media OWNER TO memoh_migrate;

REVOKE ALL ON SCHEMA iam FROM PUBLIC;
REVOKE ALL ON SCHEMA api FROM PUBLIC;
REVOKE ALL ON SCHEMA agent FROM PUBLIC;
REVOKE ALL ON SCHEMA channel FROM PUBLIC;
REVOKE ALL ON SCHEMA memory FROM PUBLIC;
REVOKE ALL ON SCHEMA runtime FROM PUBLIC;
REVOKE ALL ON SCHEMA model FROM PUBLIC;
REVOKE ALL ON SCHEMA media FROM PUBLIC;

GRANT USAGE, CREATE ON SCHEMA iam TO memoh_migrate;
GRANT USAGE, CREATE ON SCHEMA api TO memoh_migrate;
GRANT USAGE, CREATE ON SCHEMA agent TO memoh_migrate;
GRANT USAGE, CREATE ON SCHEMA channel TO memoh_migrate;
GRANT USAGE, CREATE ON SCHEMA memory TO memoh_migrate;
GRANT USAGE, CREATE ON SCHEMA runtime TO memoh_migrate;
GRANT USAGE, CREATE ON SCHEMA model TO memoh_migrate;
GRANT USAGE, CREATE ON SCHEMA media TO memoh_migrate;

GRANT USAGE ON SCHEMA iam TO memoh_iam, memoh_api, memoh_agent, memoh_channel, memoh_memory, memoh_runtime, memoh_model, memoh_media;
GRANT USAGE ON SCHEMA api TO memoh_api;
GRANT USAGE ON SCHEMA agent TO memoh_agent;
GRANT USAGE ON SCHEMA channel TO memoh_channel;
GRANT USAGE ON SCHEMA memory TO memoh_memory;
GRANT USAGE ON SCHEMA runtime TO memoh_runtime;
GRANT USAGE ON SCHEMA model TO memoh_model;
GRANT USAGE ON SCHEMA media TO memoh_media;
