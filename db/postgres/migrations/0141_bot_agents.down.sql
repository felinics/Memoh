-- 0141_bot_agents (down)
-- Remove persisted Bot Agent bindings while retaining legacy ACP descriptors.

DROP INDEX IF EXISTS public.idx_schedule_bot_agent;
DROP INDEX IF EXISTS public.idx_bot_sessions_bot_agent;

ALTER TABLE public.schedule
    DROP CONSTRAINT IF EXISTS schedule_existing_session_check,
    DROP CONSTRAINT IF EXISTS schedule_acp_fields_check,
    DROP CONSTRAINT IF EXISTS schedule_bot_agent_id_fkey,
    DROP COLUMN IF EXISTS bot_agent_id;

ALTER TABLE public.schedule
    ADD CONSTRAINT schedule_existing_session_check CHECK (
        run_target <> 'existing_session'
        OR (runtime_type IS NULL AND acp_agent_id IS NULL AND workdir_id IS NULL)
    ) NOT VALID,
    ADD CONSTRAINT schedule_acp_fields_check CHECK (
        run_target <> 'new_session'
        OR (runtime_type = 'acp_agent' AND acp_agent_id IS NOT NULL AND model_id IS NULL)
        OR (COALESCE(runtime_type, 'model') = 'model' AND acp_agent_id IS NULL AND acp_model_id IS NULL)
    ) NOT VALID;

ALTER TABLE public.bot_sessions
    DROP CONSTRAINT IF EXISTS bot_sessions_bot_agent_id_fkey,
    DROP COLUMN IF EXISTS bot_agent_id;

ALTER TABLE public.bots
    DROP CONSTRAINT IF EXISTS bots_default_bot_agent_id_fkey,
    DROP COLUMN IF EXISTS default_bot_agent_id;

DROP TABLE IF EXISTS public.bot_agents;
