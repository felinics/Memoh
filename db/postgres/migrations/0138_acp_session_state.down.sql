-- 0138_acp_session_state
-- Remove durable ACP session snapshots.

DROP TABLE IF EXISTS public.acp_session_state_lines;
DROP TABLE IF EXISTS public.acp_session_publications;
DROP TABLE IF EXISTS public.acp_session_states;
ALTER TABLE IF EXISTS public.session_runs
    DROP CONSTRAINT IF EXISTS session_runs_team_session_run_key;

-- Restore the standalone index owned by migration 0137. PostgreSQL drops an
-- attached index with its constraint; the final 0137 down migration removes
-- this replacement concurrently.
CREATE UNIQUE INDEX IF NOT EXISTS session_runs_team_session_run_key
    ON public.session_runs (team_id, session_id, run_id);
