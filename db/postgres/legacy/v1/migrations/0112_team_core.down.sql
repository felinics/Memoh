-- 0112_team_core (down)
-- Reverse the team core in dependency order.

-- ---------------------------------------------------------------------------
-- Restore secondary indexes
-- ---------------------------------------------------------------------------
-- Rebuild the team_id-leading secondary indexes back to their original form
-- (strip the leading team_id column). Only affects btree indexes whose first
-- column is team_id and whose second column onward is the original definition.

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
                 WHERE attrelid = i.indrelid AND attnum = i.indkey[0]) = 'team_id'
           AND array_length(i.indkey, 1) > 1
    LOOP
        idxdef := regexp_replace(rec.def, '(USING btree \()team_id, ', '\1');
        EXECUTE format('DROP INDEX IF EXISTS public.%I', rec.index_name);
        EXECUTE idxdef;
    END LOOP;
END
$$;

-- ---------------------------------------------------------------------------
-- Restore history view
-- ---------------------------------------------------------------------------
-- Restore the view to its pre-team shape (the 0103 definition: no team_id
-- projection, no security_invoker). This reverts to the prior state; the
-- security fix lives only in the up.

-- Column order changes (team_id-first back to turn_id-first), so drop+recreate.
DROP VIEW IF EXISTS bot_visible_history_messages;

CREATE VIEW bot_visible_history_messages AS
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

-- ---------------------------------------------------------------------------
-- Remove team row-level security
-- ---------------------------------------------------------------------------
-- Remove team policies and disable row-level security.

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
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON public.%I', tbl || '_team_select', tbl);
        EXECUTE format('DROP POLICY IF EXISTS %I ON public.%I', tbl || '_team_insert', tbl);
        EXECUTE format('DROP POLICY IF EXISTS %I ON public.%I', tbl || '_team_update', tbl);
        EXECUTE format('DROP POLICY IF EXISTS %I ON public.%I', tbl || '_team_delete', tbl);
        EXECUTE format('ALTER TABLE public.%I NO FORCE ROW LEVEL SECURITY', tbl);
        EXECUTE format('ALTER TABLE public.%I DISABLE ROW LEVEL SECURITY', tbl);
    END LOOP;
END
$$;

DROP POLICY IF EXISTS teams_self_select ON public.teams;
ALTER TABLE public.teams NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.teams DISABLE ROW LEVEL SECURITY;

-- ---------------------------------------------------------------------------
-- Restore keys and foreign keys
-- ---------------------------------------------------------------------------
-- Reverse the team-scoped unique keys and composite foreign keys.
--
-- Down safety gate: only safe on a clean singleton database (no non-default
-- team rows anywhere). If multiple teams share natural keys, restoring a
-- global UNIQUE constraints could collide. The down migration therefore only
-- restores the pre-team single-column shape and delete semantics when the
-- data is still a clean singleton.
--
DO $$
DECLARE
    rec record;
    default_team constant uuid := '00000000-0000-0000-0000-000000000001';
    orig_del text;
    orig_upd text;
    has_non_default boolean;
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
       AND a.attname = 'team_id'
       AND NOT a.attisdropped
       AND pg_get_expr(d.adbin, d.adrelid) LIKE '%memoh_current_team_id()%';

    -- Fail-closed gate: refuse if any team table holds a non-default team_id.
    FOR rec IN SELECT table_name FROM _team_tables LOOP
        EXECUTE format(
            'SELECT EXISTS (SELECT 1 FROM public.%I WHERE team_id <> %L)',
            rec.table_name, default_team
        ) INTO has_non_default;
        IF has_non_default THEN
            RAISE EXCEPTION 'refusing team-key rollback: % has non-default team rows', rec.table_name;
        END IF;
    END LOOP;

    -- Save the current composite business FKs. Their action codes still carry
    -- the exact pre-team semantics, including column-scoped SET NULL.
    CREATE TEMP TABLE _fk_saved ON COMMIT DROP AS
    SELECT c.relname AS child_table,
           con.conname AS fk_name,
           rt.relname AS parent_table,
           con.confdeltype AS del_type,
           con.confupdtype AS upd_type,
           (SELECT a.attname FROM pg_attribute a
             WHERE a.attrelid = con.conrelid AND a.attnum = con.conkey[2]) AS child_col,
           (SELECT a.attname FROM pg_attribute a
             WHERE a.attrelid = con.confrelid AND a.attnum = con.confkey[2]) AS parent_col
      FROM pg_constraint con
      JOIN pg_class c ON c.oid = con.conrelid
      JOIN pg_class rt ON rt.oid = con.confrelid
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE con.contype = 'f'
       AND n.nspname = 'public'
       AND c.relname IN (SELECT table_name FROM _team_tables)
       AND rt.relname IN (SELECT table_name FROM _team_tables)
       AND cardinality(con.conkey) = 2
       AND (SELECT a.attname FROM pg_attribute a
             WHERE a.attrelid = con.conrelid AND a.attnum = con.conkey[1]) = 'team_id';

    -- Drop the composite business FKs and team-root FKs only.
    FOR rec IN
        SELECT c.relname AS child_table, con.conname AS fk_name
          FROM pg_constraint con
          JOIN pg_class c ON c.oid = con.conrelid
          JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE con.contype = 'f' AND n.nspname = 'public'
           AND c.relname IN (SELECT table_name FROM _team_tables)
           AND EXISTS (
               SELECT 1 FROM pg_attribute a
                WHERE a.attrelid = con.conrelid
                  AND a.attnum = ANY(con.conkey)
                  AND a.attname = 'team_id'
           )
    LOOP
        EXECUTE format('ALTER TABLE public.%I DROP CONSTRAINT %I', rec.child_table, rec.fk_name);
    END LOOP;

    -- Drop the helper unique keys added for unchanged primary keys.
    FOR rec IN
        SELECT c.relname AS table_name, con.conname
          FROM pg_constraint con
          JOIN pg_class c ON c.oid = con.conrelid
          JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE con.contype = 'u' AND n.nspname = 'public'
           AND c.relname IN (SELECT table_name FROM _team_tables)
           AND con.conname LIKE 'memoh_team_key_%'
    LOOP
        EXECUTE format('ALTER TABLE public.%I DROP CONSTRAINT %I', rec.table_name, rec.conname);
    END LOOP;

    -- Restore single-column UNIQUE constraints (strip leading team_id).
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
        IF (SELECT attname FROM pg_attribute WHERE attrelid=rec.conrelid AND attnum=rec.conkey[1]) <> 'team_id' THEN
            CONTINUE;
        END IF;
        EXECUTE format('ALTER TABLE public.%I DROP CONSTRAINT %I', rec.table_name, rec.conname);
        EXECUTE format('ALTER TABLE public.%I ADD CONSTRAINT %I UNIQUE %s (%s)',
            rec.table_name, rec.conname,
            CASE WHEN rec.nulls_not_distinct THEN 'NULLS NOT DISTINCT' ELSE '' END,
            (SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY k.ord)
               FROM unnest(rec.conkey) WITH ORDINALITY AS k(attnum, ord)
               JOIN pg_attribute a ON a.attrelid=rec.conrelid AND a.attnum=k.attnum
              WHERE k.ord > 1));
    END LOOP;

    -- Recreate the original single-column FKs with their exact actions.
    FOR rec IN SELECT * FROM _fk_saved LOOP
        orig_del := CASE rec.del_type WHEN 'c' THEN 'CASCADE' WHEN 'r' THEN 'RESTRICT'
                                      WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT'
                                      ELSE 'NO ACTION' END;
        orig_upd := CASE rec.upd_type WHEN 'c' THEN 'CASCADE' WHEN 'r' THEN 'RESTRICT'
                                      WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT'
                                      ELSE 'NO ACTION' END;
        EXECUTE format(
            'ALTER TABLE public.%I ADD CONSTRAINT %I FOREIGN KEY (%I) REFERENCES public.%I (%I) ON UPDATE %s ON DELETE %s',
            rec.child_table, rec.fk_name, rec.child_col, rec.parent_table, rec.parent_col, orig_upd, orig_del);
    END LOOP;

END
$$;

-- Restore the partial / expression unique indexes to their original (global) shape.
DROP INDEX IF EXISTS idx_bot_channel_external_identity;
CREATE UNIQUE INDEX idx_bot_channel_external_identity
    ON public.bot_channel_configs (channel_type, external_identity);

DROP INDEX IF EXISTS idx_bot_channel_routes_unique;
CREATE UNIQUE INDEX idx_bot_channel_routes_unique
    ON public.bot_channel_routes
       (bot_id, channel_type, external_conversation_id, COALESCE(external_thread_id, ''::text));

DROP INDEX IF EXISTS idx_bot_history_messages_turn_seq_unique;
CREATE UNIQUE INDEX idx_bot_history_messages_turn_seq_unique
    ON public.bot_history_messages (turn_id, turn_message_seq)
    WHERE ((turn_id IS NOT NULL) AND (turn_message_seq IS NOT NULL));

DROP INDEX IF EXISTS idx_bot_user_grants_unique_everyone;
CREATE UNIQUE INDEX idx_bot_user_grants_unique_everyone
    ON public.bot_user_grants (bot_id)
    WHERE (subject_type = 'everyone'::text);

DROP INDEX IF EXISTS idx_bot_user_grants_unique_user;
CREATE UNIQUE INDEX idx_bot_user_grants_unique_user
    ON public.bot_user_grants (bot_id, user_id)
    WHERE (subject_type = 'user'::text);

DROP INDEX IF EXISTS idx_bots_name;
CREATE UNIQUE INDEX idx_bots_name
    ON public.bots (name);

DROP INDEX IF EXISTS idx_session_events_dedup;
CREATE UNIQUE INDEX idx_session_events_dedup
    ON public.bot_session_events (session_id, event_kind, external_message_id)
    WHERE ((external_message_id IS NOT NULL) AND (external_message_id <> ''::text));

DROP INDEX IF EXISTS idx_snapshots_container_runtime_name;
CREATE UNIQUE INDEX idx_snapshots_container_runtime_name
    ON public.snapshots (container_id, runtime_snapshot_name);

-- ---------------------------------------------------------------------------
-- Remove team columns
-- ---------------------------------------------------------------------------
-- Drop the team_id column from every team business table.
--
-- Down safety gate: only allow rollback on a clean singleton database — no
-- row in any team table may carry a non-default team_id. If a non-default
-- team's data exists, dropping team_id would destroy the only isolation
-- marker, so we fail closed (never wipe as rollback).

DO $$
DECLARE
    tbl text;
    default_team constant uuid := '00000000-0000-0000-0000-000000000001';
    bad bigint;
BEGIN
    -- Fail-closed gate: refuse if any team table holds non-default team_id.
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
    LOOP
        EXECUTE format(
            'SELECT count(*) FROM public.%I WHERE team_id IS NOT NULL AND team_id <> %L',
            tbl, default_team
        ) INTO bad;
        IF bad > 0 THEN
            RAISE EXCEPTION
                'refusing to drop team_id from %: % rows carry a non-default team_id (multi-team database)',
                tbl, bad;
        END IF;
    END LOOP;

    -- Safe: drop only columns introduced by the matching up migration. User
    -- tables that happen to define their own team_id are left untouched.
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
        EXECUTE format('ALTER TABLE public.%I DROP COLUMN IF EXISTS team_id', tbl);
    END LOOP;
END
$$;

-- ---------------------------------------------------------------------------
-- Remove team context
-- ---------------------------------------------------------------------------
-- Remove the public team context helper.

DROP FUNCTION IF EXISTS public.memoh_current_team_id();

-- ---------------------------------------------------------------------------
-- Remove team root
-- ---------------------------------------------------------------------------
-- Reverse the teams root introduction.
--
-- Down safety gate: only allow rollback when the database is still a clean
-- singleton — i.e. there is no team other than the default. If any
-- non-default team exists, dropping the root would orphan team data, so we
-- fail closed rather
-- than silently destroy data. (A wipe is never an acceptable rollback.)

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM teams
        WHERE id <> '00000000-0000-0000-0000-000000000001'::uuid
    ) THEN
        RAISE EXCEPTION
            'refusing to drop teams root: non-default team rows present (multi-team database)';
    END IF;
END
$$;

DROP INDEX IF EXISTS teams_slug_unique;
DROP TABLE IF EXISTS teams;
