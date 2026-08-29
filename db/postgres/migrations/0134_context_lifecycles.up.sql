-- 0134_context_lifecycles
-- Persist content-light context lifecycle snapshots by run, including failures.

CREATE TABLE IF NOT EXISTS public.context_lifecycles (
    run_id     UUID        PRIMARY KEY,
    team_id    UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                            REFERENCES public.teams(id) ON DELETE RESTRICT,
    bot_id     UUID        NOT NULL,
    session_id UUID        NOT NULL,
    status     TEXT        NOT NULL,
    error_code TEXT,
    snapshot   JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT context_lifecycles_team_run_key UNIQUE (team_id, run_id),
    CONSTRAINT context_lifecycles_status_check CHECK (status IN (
        'completed', 'failed_budget', 'failed_provider', 'fallback', 'aborted'
    )),
    CONSTRAINT context_lifecycles_bot_id_fkey
        FOREIGN KEY (team_id, bot_id)
        REFERENCES public.bots(team_id, id) ON DELETE CASCADE,
    CONSTRAINT context_lifecycles_session_id_fkey
        FOREIGN KEY (team_id, session_id)
        REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_context_lifecycles_session_recent
    ON public.context_lifecycles (team_id, session_id, created_at DESC, run_id DESC);

ALTER TABLE public.context_lifecycles ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.context_lifecycles FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS context_lifecycles_team_select ON public.context_lifecycles;
DROP POLICY IF EXISTS context_lifecycles_team_insert ON public.context_lifecycles;
DROP POLICY IF EXISTS context_lifecycles_team_update ON public.context_lifecycles;
DROP POLICY IF EXISTS context_lifecycles_team_delete ON public.context_lifecycles;

CREATE POLICY context_lifecycles_team_select ON public.context_lifecycles
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY context_lifecycles_team_insert ON public.context_lifecycles
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY context_lifecycles_team_update ON public.context_lifecycles
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY context_lifecycles_team_delete ON public.context_lifecycles
    FOR DELETE USING (team_id = public.memoh_current_team_id());
