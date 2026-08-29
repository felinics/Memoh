-- 0137_acp_session_run_index
-- Remove the standalone candidate-key index after 0138 has detached it from
-- the unique constraint.

DROP INDEX CONCURRENTLY IF EXISTS public.session_runs_team_session_run_key;
