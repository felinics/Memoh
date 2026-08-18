-- 0140_drop_heartbeat
-- Restore the retired heartbeat schema. Removed runtime data is not recoverable.

ALTER TABLE public.bot_history_messages NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_history_messages DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots DISABLE ROW LEVEL SECURITY;

ALTER TABLE public.bots
    ADD COLUMN IF NOT EXISTS heartbeat_enabled BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS heartbeat_interval INTEGER NOT NULL DEFAULT 1440,
    ADD COLUMN IF NOT EXISTS heartbeat_prompt TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS heartbeat_model_id UUID;

DO $bots_heartbeat_model_fk$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.bots'::regclass
          AND conname = 'bots_heartbeat_model_id_fkey'
    ) THEN
        ALTER TABLE public.bots
            ADD CONSTRAINT bots_heartbeat_model_id_fkey
            FOREIGN KEY (team_id, heartbeat_model_id)
            REFERENCES public.models(team_id, id)
            ON DELETE SET NULL (heartbeat_model_id);
    END IF;
END
$bots_heartbeat_model_fk$;

CREATE TABLE IF NOT EXISTS public.bot_heartbeat_logs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id UUID NOT NULL DEFAULT public.memoh_current_team_id(),
    bot_id UUID NOT NULL,
    session_id UUID,
    status TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok', 'alert', 'error')),
    result_text TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    usage JSONB,
    model_id UUID,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    CONSTRAINT bot_heartbeat_logs_team_id_fkey
        FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT,
    CONSTRAINT bot_heartbeat_logs_bot_id_fkey
        FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE,
    CONSTRAINT bot_heartbeat_logs_session_id_fkey
        FOREIGN KEY (team_id, session_id) REFERENCES public.bot_sessions(team_id, id)
        ON DELETE SET NULL (session_id),
    CONSTRAINT bot_heartbeat_logs_model_id_fkey
        FOREIGN KEY (team_id, model_id) REFERENCES public.models(team_id, id)
        ON DELETE SET NULL (model_id),
    CONSTRAINT memoh_team_key_bot_heartbeat_logs UNIQUE (team_id, id)
);

CREATE INDEX IF NOT EXISTS idx_heartbeat_logs_bot_started
    ON public.bot_heartbeat_logs(team_id, bot_id, started_at DESC);

ALTER TABLE public.bot_heartbeat_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_heartbeat_logs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS bot_heartbeat_logs_team_select ON public.bot_heartbeat_logs;
DROP POLICY IF EXISTS bot_heartbeat_logs_team_insert ON public.bot_heartbeat_logs;
DROP POLICY IF EXISTS bot_heartbeat_logs_team_update ON public.bot_heartbeat_logs;
DROP POLICY IF EXISTS bot_heartbeat_logs_team_delete ON public.bot_heartbeat_logs;
CREATE POLICY bot_heartbeat_logs_team_select ON public.bot_heartbeat_logs
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_heartbeat_logs_team_insert ON public.bot_heartbeat_logs
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_heartbeat_logs_team_update ON public.bot_heartbeat_logs
    FOR UPDATE USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_heartbeat_logs_team_delete ON public.bot_heartbeat_logs
    FOR DELETE USING (team_id = public.memoh_current_team_id());

ALTER TABLE public.bot_sessions
    DROP CONSTRAINT IF EXISTS bot_sessions_type_check,
    DROP CONSTRAINT IF EXISTS bot_sessions_session_mode_check,
    ADD CONSTRAINT bot_sessions_type_check
        CHECK (type IN ('chat', 'heartbeat', 'schedule', 'subagent', 'discuss', 'acp_agent')),
    ADD CONSTRAINT bot_sessions_session_mode_check
        CHECK (session_mode IN ('chat', 'discuss', 'heartbeat', 'schedule', 'subagent'));

ALTER TABLE public.bot_history_messages
    DROP CONSTRAINT IF EXISTS bot_history_messages_session_mode_check,
    ADD CONSTRAINT bot_history_messages_session_mode_check
        CHECK (session_mode IN ('chat', 'discuss', 'heartbeat', 'schedule', 'subagent'));

ALTER TABLE public.bot_history_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_history_messages FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;
