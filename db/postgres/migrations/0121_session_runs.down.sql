-- 0121_session_runs
-- Drop the session run ledger and the run/turn traceability columns.

DROP INDEX IF EXISTS idx_bot_history_messages_run;
DROP INDEX IF EXISTS idx_user_input_run;
DROP INDEX IF EXISTS idx_tool_approval_run;

ALTER TABLE public.bot_history_messages
    DROP COLUMN IF EXISTS run_id;

ALTER TABLE public.user_input_requests
    DROP COLUMN IF EXISTS turn_id,
    DROP COLUMN IF EXISTS run_id;

ALTER TABLE public.tool_approval_requests
    DROP COLUMN IF EXISTS turn_id,
    DROP COLUMN IF EXISTS run_id;

DROP TABLE IF EXISTS public.session_runs;
