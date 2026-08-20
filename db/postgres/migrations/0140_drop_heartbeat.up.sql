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

-- Subagents can form a session tree below a Heartbeat turn. Retire the whole
-- tree instead of letting ON DELETE SET NULL detach descendants from the
-- removed parent and leave their runtime data behind.
WITH RECURSIVE retired_sessions(team_id, id) AS (
    SELECT session.team_id, session.id
    FROM public.bot_sessions AS session
    WHERE session.type = 'heartbeat'
       OR session.session_mode = 'heartbeat'
    UNION
    SELECT child.team_id, child.id
    FROM public.bot_sessions AS child
    JOIN retired_sessions AS parent
      ON parent.team_id = child.team_id
     AND parent.id = child.parent_session_id
)
DELETE FROM public.bot_history_messages AS message
WHERE message.session_mode = 'heartbeat'
   OR (message.team_id, message.session_id) IN (
       SELECT retired.team_id, retired.id
       FROM retired_sessions AS retired
   );

WITH RECURSIVE retired_sessions(team_id, id) AS (
    SELECT session.team_id, session.id
    FROM public.bot_sessions AS session
    WHERE session.type = 'heartbeat'
       OR session.session_mode = 'heartbeat'
    UNION
    SELECT child.team_id, child.id
    FROM public.bot_sessions AS child
    JOIN retired_sessions AS parent
      ON parent.team_id = child.team_id
     AND parent.id = child.parent_session_id
)
DELETE FROM public.bot_sessions AS session
WHERE (session.team_id, session.id) IN (
    SELECT retired.team_id, retired.id
    FROM retired_sessions AS retired
);

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
