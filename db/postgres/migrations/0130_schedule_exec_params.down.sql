-- 0130_schedule_exec_params
-- Drop schedule execution parameter columns and the bot_sessions.visibility
-- column.

ALTER TABLE public.bot_sessions DROP CONSTRAINT IF EXISTS bot_sessions_visibility_check;
ALTER TABLE public.bot_sessions DROP COLUMN IF EXISTS visibility;

ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_workdir_id_fkey;
ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_model_id_fkey;
ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_target_session_id_fkey;
ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_model_exclusive_check;
ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_acp_fields_check;
ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_new_session_check;
ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_existing_session_check;
ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_runtime_type_check;
ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_run_target_check;

ALTER TABLE public.schedule
  DROP COLUMN IF EXISTS workdir_id,
  DROP COLUMN IF EXISTS reasoning_effort,
  DROP COLUMN IF EXISTS acp_model_id,
  DROP COLUMN IF EXISTS model_id,
  DROP COLUMN IF EXISTS acp_agent_id,
  DROP COLUMN IF EXISTS runtime_type,
  DROP COLUMN IF EXISTS target_session_id,
  DROP COLUMN IF EXISTS run_target;
