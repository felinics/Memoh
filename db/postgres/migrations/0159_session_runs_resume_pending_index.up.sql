-- 0158_session_runs_resume_pending_index
-- Index the interrupted runs that still carry a resume intent. The recovery
-- worker lists them every few seconds; without this partial index the query
-- walks a team's entire run history. This migration must remain a single
-- statement: golang-migrate then executes CREATE INDEX CONCURRENTLY outside an
-- implicit transaction.

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_runs_resume_pending
    ON public.session_runs (team_id, run_id)
    WHERE state = 'lost' AND error_code = 'session_runtime.interrupted' AND input_json ? 'resume';
