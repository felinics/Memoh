-- 0158_session_runs_resume_pending_index
-- Remove the pending-resume partial index.

DROP INDEX CONCURRENTLY IF EXISTS public.idx_session_runs_resume_pending;
