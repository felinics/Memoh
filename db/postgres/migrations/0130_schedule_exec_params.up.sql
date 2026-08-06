-- 0130_schedule_exec_params
-- Schedule execution parameters: where a fire runs (new session vs an
-- existing session), which runtime/model executes it (native provider model
-- or an ACP agent + agent model), a per-schedule reasoning effort, and a
-- workdir binding for new sessions. Also makes bot_sessions.visibility a
-- real column so schedule-created sessions can surface in user-facing
-- session lists without widening the legacy type filter.

-- schedule: execution parameter columns.
--
-- Model is two columns on purpose: native models are FK-able UUIDs
-- (models.id), ACP models are agent-reported strings with no backing table.
--
-- existing_session mode pins runtime and workdir via the target session, so
-- runtime_type / acp_agent_id / workdir_id must stay NULL in that mode;
-- model_id / acp_model_id / reasoning_effort remain overridable (which of
-- the two model columns applies depends on the target session's runtime and
-- is validated in the service layer, where the session row is visible).
ALTER TABLE public.schedule
  ADD COLUMN IF NOT EXISTS run_target        TEXT NOT NULL DEFAULT 'new_session',
  ADD COLUMN IF NOT EXISTS target_session_id UUID,
  ADD COLUMN IF NOT EXISTS runtime_type      TEXT,
  ADD COLUMN IF NOT EXISTS acp_agent_id      TEXT,
  ADD COLUMN IF NOT EXISTS model_id          UUID,
  ADD COLUMN IF NOT EXISTS acp_model_id      TEXT,
  ADD COLUMN IF NOT EXISTS reasoning_effort  TEXT,
  ADD COLUMN IF NOT EXISTS workdir_id        UUID;

ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_run_target_check;
ALTER TABLE public.schedule ADD CONSTRAINT schedule_run_target_check
  CHECK (run_target IN ('new_session', 'existing_session'));

ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_runtime_type_check;
ALTER TABLE public.schedule ADD CONSTRAINT schedule_runtime_type_check
  CHECK (runtime_type IS NULL OR runtime_type IN ('model', 'acp_agent'));

-- existing_session inherits runtime and workdir from the target session.
-- target_session_id is deliberately NOT required here: the FK below degrades
-- it to NULL when the target session is hard-deleted, and the trigger path
-- reports that state and disables the schedule instead of failing the delete.
ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_existing_session_check;
ALTER TABLE public.schedule ADD CONSTRAINT schedule_existing_session_check
  CHECK (
    run_target <> 'existing_session'
    OR (runtime_type IS NULL AND acp_agent_id IS NULL AND workdir_id IS NULL)
  );

ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_new_session_check;
ALTER TABLE public.schedule ADD CONSTRAINT schedule_new_session_check
  CHECK (run_target <> 'new_session' OR target_session_id IS NULL);

-- new_session mode: an ACP schedule names its agent and may only use the ACP
-- model column; a native schedule may only use the native model column.
ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_acp_fields_check;
ALTER TABLE public.schedule ADD CONSTRAINT schedule_acp_fields_check
  CHECK (
    run_target <> 'new_session'
    OR (runtime_type = 'acp_agent' AND acp_agent_id IS NOT NULL AND model_id IS NULL)
    OR (COALESCE(runtime_type, 'model') = 'model' AND acp_agent_id IS NULL AND acp_model_id IS NULL)
  );

ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_model_exclusive_check;
ALTER TABLE public.schedule ADD CONSTRAINT schedule_model_exclusive_check
  CHECK (NOT (model_id IS NOT NULL AND acp_model_id IS NOT NULL));

-- Composite team FKs, NOT VALID to skip the validation scan: the columns
-- were added NULL in this very migration, so validation is vacuous — and the
-- scan would evaluate the referenced tables' FORCE RLS policies, which raise
-- when the migration role has no memoh.team_id GUC set.
ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_target_session_id_fkey;
ALTER TABLE public.schedule
  ADD CONSTRAINT schedule_target_session_id_fkey
  FOREIGN KEY (team_id, target_session_id)
  REFERENCES public.bot_sessions(team_id, id) ON DELETE SET NULL (target_session_id)
  NOT VALID;

ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_model_id_fkey;
ALTER TABLE public.schedule
  ADD CONSTRAINT schedule_model_id_fkey
  FOREIGN KEY (team_id, model_id)
  REFERENCES public.models(team_id, id) ON DELETE SET NULL (model_id)
  NOT VALID;

ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_workdir_id_fkey;
ALTER TABLE public.schedule
  ADD CONSTRAINT schedule_workdir_id_fkey
  FOREIGN KEY (team_id, workdir_id)
  REFERENCES public.bot_workdirs(team_id, id) ON DELETE SET NULL (workdir_id)
  NOT VALID;

-- bot_sessions.visibility: promote the previously implicit "user-facing"
-- notion (a hardcoded type list in the session list endpoints) to a stored
-- column. Sessions created by schedules keep session_mode='schedule' for
-- prompt/tool gating but can now opt into user-facing listings.
ALTER TABLE public.bot_sessions
  ADD COLUMN IF NOT EXISTS visibility TEXT NOT NULL DEFAULT 'internal';

ALTER TABLE public.bot_sessions DROP CONSTRAINT IF EXISTS bot_sessions_visibility_check;
ALTER TABLE public.bot_sessions ADD CONSTRAINT bot_sessions_visibility_check
  CHECK (visibility IN ('user', 'internal'));

-- Backfill mirrors the legacy UserFacingSessionTypes() filter exactly:
-- historical schedule/heartbeat/subagent sessions stay internal. FORCE RLS
-- would hide every row from the migration role (no memoh.team_id GUC), so
-- suspend it for the UPDATE, matching the 0001 team-migration pattern.
ALTER TABLE public.bot_sessions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions DISABLE ROW LEVEL SECURITY;

UPDATE public.bot_sessions SET visibility = 'user'
 WHERE type IN ('chat', 'discuss', 'acp_agent');

ALTER TABLE public.bot_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions FORCE ROW LEVEL SECURITY;
