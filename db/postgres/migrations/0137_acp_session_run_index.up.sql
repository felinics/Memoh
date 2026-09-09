-- 0137_acp_session_run_index
-- Build the candidate key needed by ACP session-state foreign keys without
-- blocking writes to a populated session_runs table for the duration of the
-- index build. This migration must remain a single statement: golang-migrate
-- then executes CREATE INDEX CONCURRENTLY outside an implicit transaction.

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS session_runs_team_session_run_key
    ON public.session_runs (team_id, session_id, run_id);
