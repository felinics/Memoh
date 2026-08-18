-- 0140_drop_heartbeat
-- Remove the retired heartbeat scheduler, configuration, logs, and runtime data.

-- Migration owners are subject to FORCE RLS, so temporarily suspend it while
-- deleting heartbeat rows across every team and rebuilding constraints.
ALTER TABLE public.bot_history_messages NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_history_messages DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots DISABLE ROW LEVEL SECURITY;

DELETE FROM public.bot_history_messages AS message
WHERE message.session_mode = 'heartbeat'
   OR message.session_id IN (
       SELECT session.id
       FROM public.bot_sessions AS session
       WHERE session.type = 'heartbeat'
          OR session.session_mode = 'heartbeat'
   );

DELETE FROM public.bot_sessions
WHERE type = 'heartbeat'
   OR session_mode = 'heartbeat';

ALTER TABLE public.bot_sessions
    DROP CONSTRAINT IF EXISTS bot_sessions_type_check,
    DROP CONSTRAINT IF EXISTS bot_sessions_session_mode_check,
    ADD CONSTRAINT bot_sessions_type_check
        CHECK (type IN ('chat', 'schedule', 'subagent', 'discuss', 'acp_agent')),
    ADD CONSTRAINT bot_sessions_session_mode_check
        CHECK (session_mode IN ('chat', 'discuss', 'schedule', 'subagent'));

ALTER TABLE public.bot_history_messages
    DROP CONSTRAINT IF EXISTS bot_history_messages_session_mode_check,
    ADD CONSTRAINT bot_history_messages_session_mode_check
        CHECK (session_mode IN ('chat', 'discuss', 'schedule', 'subagent'));

DROP TABLE IF EXISTS public.bot_heartbeat_logs;

ALTER TABLE public.bots
    DROP COLUMN IF EXISTS heartbeat_enabled,
    DROP COLUMN IF EXISTS heartbeat_interval,
    DROP COLUMN IF EXISTS heartbeat_prompt,
    DROP COLUMN IF EXISTS heartbeat_model_id;

ALTER TABLE public.bot_history_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_history_messages FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;
