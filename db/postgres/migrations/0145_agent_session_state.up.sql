-- 0145_agent_session_state
-- Generalize the ACP-named checkpoint storage to External Agent names so
-- ACP, Codex, and Claude Code share one store.
--
-- Two paths per table, both idempotent:
--   * Upgrade (only acp_* exists): pure rename — data, constraints, and
--     policies are preserved.
--   * Fresh chain (both exist): 0001 already created the final agent_*
--     schema and the frozen 0138 recreated the legacy acp_* tables empty
--     afterwards; drop the unused empty duplicates. A non-empty duplicate is
--     an impossible state and fails loudly instead of losing data.

DO $$
DECLARE
    constraint_name text;
    target_name text;
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = 'public' AND table_name = 'acp_session_states'
    ) THEN
        IF EXISTS (
            SELECT 1 FROM information_schema.tables
            WHERE table_schema = 'public' AND table_name = 'agent_session_states'
        ) THEN
            -- The probe scans a table under FORCE row level security; lift it
            -- first (the table is dropped below, and an abort rolls this back).
            ALTER TABLE public.acp_session_states NO FORCE ROW LEVEL SECURITY;
            ALTER TABLE public.acp_session_states DISABLE ROW LEVEL SECURITY;
            IF EXISTS (SELECT 1 FROM public.acp_session_states) THEN
                RAISE EXCEPTION 'both acp_session_states and agent_session_states exist and the legacy table has data';
            END IF;
            DROP TABLE public.acp_session_states;
        ELSE
            ALTER TABLE public.acp_session_states RENAME TO agent_session_states;
            ALTER TABLE public.agent_session_states RENAME COLUMN acp_session_id TO agent_session_id;
            -- A table rename does not rename its constraints. Rename every
            -- ACP-prefixed constraint, including PostgreSQL-generated team FK
            -- and NOT NULL names, so upgraded and canonical schemas match.
            FOR constraint_name IN
                SELECT conname::text
                FROM pg_constraint
                WHERE conrelid = 'public.agent_session_states'::regclass
                  AND left(conname, length('acp_session_states_')) = 'acp_session_states_'
            LOOP
                target_name := 'agent_' || substring(constraint_name FROM length('acp_') + 1);
                target_name := replace(
                    target_name,
                    'agent_session_states_acp_session_id_',
                    'agent_session_states_agent_session_id_'
                );
                EXECUTE format(
                    'ALTER TABLE public.agent_session_states RENAME CONSTRAINT %I TO %I',
                    constraint_name,
                    target_name
                );
            END LOOP;
            ALTER POLICY acp_session_states_team_select ON public.agent_session_states RENAME TO agent_session_states_team_select;
            ALTER POLICY acp_session_states_team_insert ON public.agent_session_states RENAME TO agent_session_states_team_insert;
            ALTER POLICY acp_session_states_team_update ON public.agent_session_states RENAME TO agent_session_states_team_update;
            ALTER POLICY acp_session_states_team_delete ON public.agent_session_states RENAME TO agent_session_states_team_delete;
        END IF;
    END IF;
END $$;

DO $$
DECLARE
    constraint_name text;
    target_name text;
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = 'public' AND table_name = 'acp_session_state_lines'
    ) THEN
        IF EXISTS (
            SELECT 1 FROM information_schema.tables
            WHERE table_schema = 'public' AND table_name = 'agent_session_state_lines'
        ) THEN
            -- The probe scans a table under FORCE row level security; lift it
            -- first (the table is dropped below, and an abort rolls this back).
            ALTER TABLE public.acp_session_state_lines NO FORCE ROW LEVEL SECURITY;
            ALTER TABLE public.acp_session_state_lines DISABLE ROW LEVEL SECURITY;
            IF EXISTS (SELECT 1 FROM public.acp_session_state_lines) THEN
                RAISE EXCEPTION 'both acp_session_state_lines and agent_session_state_lines exist and the legacy table has data';
            END IF;
            DROP TABLE public.acp_session_state_lines;
        ELSE
            ALTER TABLE public.acp_session_state_lines RENAME TO agent_session_state_lines;
            FOR constraint_name IN
                SELECT conname::text
                FROM pg_constraint
                WHERE conrelid = 'public.agent_session_state_lines'::regclass
                  AND left(conname, length('acp_session_state_lines_')) = 'acp_session_state_lines_'
            LOOP
                target_name := 'agent_' || substring(constraint_name FROM length('acp_') + 1);
                EXECUTE format(
                    'ALTER TABLE public.agent_session_state_lines RENAME CONSTRAINT %I TO %I',
                    constraint_name,
                    target_name
                );
            END LOOP;
            ALTER POLICY acp_session_state_lines_team_select ON public.agent_session_state_lines RENAME TO agent_session_state_lines_team_select;
            ALTER POLICY acp_session_state_lines_team_insert ON public.agent_session_state_lines RENAME TO agent_session_state_lines_team_insert;
            ALTER POLICY acp_session_state_lines_team_update ON public.agent_session_state_lines RENAME TO agent_session_state_lines_team_update;
            ALTER POLICY acp_session_state_lines_team_delete ON public.agent_session_state_lines RENAME TO agent_session_state_lines_team_delete;
        END IF;
    END IF;
END $$;

DO $$
DECLARE
    constraint_name text;
    target_name text;
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = 'public' AND table_name = 'acp_session_publications'
    ) THEN
        IF EXISTS (
            SELECT 1 FROM information_schema.tables
            WHERE table_schema = 'public' AND table_name = 'agent_session_publications'
        ) THEN
            -- The probe scans a table under FORCE row level security; lift it
            -- first (the table is dropped below, and an abort rolls this back).
            ALTER TABLE public.acp_session_publications NO FORCE ROW LEVEL SECURITY;
            ALTER TABLE public.acp_session_publications DISABLE ROW LEVEL SECURITY;
            IF EXISTS (SELECT 1 FROM public.acp_session_publications) THEN
                RAISE EXCEPTION 'both acp_session_publications and agent_session_publications exist and the legacy table has data';
            END IF;
            DROP TABLE public.acp_session_publications;
        ELSE
            ALTER TABLE public.acp_session_publications RENAME TO agent_session_publications;
            FOR constraint_name IN
                SELECT conname::text
                FROM pg_constraint
                WHERE conrelid = 'public.agent_session_publications'::regclass
                  AND left(conname, length('acp_session_publications_')) = 'acp_session_publications_'
            LOOP
                target_name := 'agent_' || substring(constraint_name FROM length('acp_') + 1);
                EXECUTE format(
                    'ALTER TABLE public.agent_session_publications RENAME CONSTRAINT %I TO %I',
                    constraint_name,
                    target_name
                );
            END LOOP;
            ALTER POLICY acp_session_publications_team_select ON public.agent_session_publications RENAME TO agent_session_publications_team_select;
            ALTER POLICY acp_session_publications_team_insert ON public.agent_session_publications RENAME TO agent_session_publications_team_insert;
            ALTER POLICY acp_session_publications_team_update ON public.agent_session_publications RENAME TO agent_session_publications_team_update;
            ALTER POLICY acp_session_publications_team_delete ON public.agent_session_publications RENAME TO agent_session_publications_team_delete;
        END IF;
    END IF;
END $$;
