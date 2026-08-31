-- 0145_agent_session_state
-- Revert the External Agent renames back to the ACP-scoped names. Each
-- block is guarded on the new table name so the rollback is safe to re-run.

DO $$
DECLARE
    constraint_name text;
    target_name text;
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = 'public' AND table_name = 'agent_session_publications'
    ) THEN
        ALTER POLICY agent_session_publications_team_select ON public.agent_session_publications RENAME TO acp_session_publications_team_select;
        ALTER POLICY agent_session_publications_team_insert ON public.agent_session_publications RENAME TO acp_session_publications_team_insert;
        ALTER POLICY agent_session_publications_team_update ON public.agent_session_publications RENAME TO acp_session_publications_team_update;
        ALTER POLICY agent_session_publications_team_delete ON public.agent_session_publications RENAME TO acp_session_publications_team_delete;
        FOR constraint_name IN
            SELECT conname::text
            FROM pg_constraint
            WHERE conrelid = 'public.agent_session_publications'::regclass
              AND left(conname, length('agent_session_publications_')) = 'agent_session_publications_'
        LOOP
            target_name := 'acp_' || substring(constraint_name FROM length('agent_') + 1);
            EXECUTE format(
                'ALTER TABLE public.agent_session_publications RENAME CONSTRAINT %I TO %I',
                constraint_name,
                target_name
            );
        END LOOP;
        ALTER TABLE public.agent_session_publications RENAME TO acp_session_publications;
    END IF;
END $$;

DO $$
DECLARE
    constraint_name text;
    target_name text;
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = 'public' AND table_name = 'agent_session_state_lines'
    ) THEN
        ALTER POLICY agent_session_state_lines_team_select ON public.agent_session_state_lines RENAME TO acp_session_state_lines_team_select;
        ALTER POLICY agent_session_state_lines_team_insert ON public.agent_session_state_lines RENAME TO acp_session_state_lines_team_insert;
        ALTER POLICY agent_session_state_lines_team_update ON public.agent_session_state_lines RENAME TO acp_session_state_lines_team_update;
        ALTER POLICY agent_session_state_lines_team_delete ON public.agent_session_state_lines RENAME TO acp_session_state_lines_team_delete;
        FOR constraint_name IN
            SELECT conname::text
            FROM pg_constraint
            WHERE conrelid = 'public.agent_session_state_lines'::regclass
              AND left(conname, length('agent_session_state_lines_')) = 'agent_session_state_lines_'
        LOOP
            target_name := 'acp_' || substring(constraint_name FROM length('agent_') + 1);
            EXECUTE format(
                'ALTER TABLE public.agent_session_state_lines RENAME CONSTRAINT %I TO %I',
                constraint_name,
                target_name
            );
        END LOOP;
        ALTER TABLE public.agent_session_state_lines RENAME TO acp_session_state_lines;
    END IF;
END $$;

DO $$
DECLARE
    constraint_name text;
    target_name text;
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = 'public' AND table_name = 'agent_session_states'
    ) THEN
        ALTER POLICY agent_session_states_team_select ON public.agent_session_states RENAME TO acp_session_states_team_select;
        ALTER POLICY agent_session_states_team_insert ON public.agent_session_states RENAME TO acp_session_states_team_insert;
        ALTER POLICY agent_session_states_team_update ON public.agent_session_states RENAME TO acp_session_states_team_update;
        ALTER POLICY agent_session_states_team_delete ON public.agent_session_states RENAME TO acp_session_states_team_delete;
        FOR constraint_name IN
            SELECT conname::text
            FROM pg_constraint
            WHERE conrelid = 'public.agent_session_states'::regclass
              AND left(conname, length('agent_session_states_')) = 'agent_session_states_'
        LOOP
            target_name := 'acp_' || substring(constraint_name FROM length('agent_') + 1);
            target_name := replace(
                target_name,
                'acp_session_states_agent_session_id_',
                'acp_session_states_acp_session_id_'
            );
            EXECUTE format(
                'ALTER TABLE public.agent_session_states RENAME CONSTRAINT %I TO %I',
                constraint_name,
                target_name
            );
        END LOOP;
        ALTER TABLE public.agent_session_states RENAME COLUMN agent_session_id TO acp_session_id;
        ALTER TABLE public.agent_session_states RENAME TO acp_session_states;
    END IF;
END $$;
