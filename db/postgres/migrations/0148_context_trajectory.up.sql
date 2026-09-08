-- 0148_context_trajectory
-- Persist ordered context stages and full content atomically, outside lifecycle summaries.

CREATE TABLE IF NOT EXISTS public.context_trajectory_contents (
    team_id UUID NOT NULL DEFAULT public.memoh_current_team_id()
        REFERENCES public.teams(id) ON DELETE RESTRICT,
    bot_id UUID NOT NULL,
    session_id UUID NOT NULL,
    content_hash TEXT NOT NULL,
    content BYTEA NOT NULL,
    PRIMARY KEY (team_id, bot_id, session_id, content_hash),
    FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE,
    FOREIGN KEY (team_id, session_id) REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_context_trajectory_contents_session
    ON public.context_trajectory_contents (team_id, session_id);

CREATE TABLE IF NOT EXISTS public.context_trajectory_events (
    id BIGSERIAL PRIMARY KEY,
    team_id UUID NOT NULL DEFAULT public.memoh_current_team_id()
        REFERENCES public.teams(id) ON DELETE RESTRICT,
    bot_id UUID NOT NULL,
    session_id UUID NOT NULL,
    run_id UUID NOT NULL,
    capture_id UUID NOT NULL,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    event JSONB NOT NULL CHECK (
        jsonb_typeof(event) = 'object'
        AND jsonb_typeof(event->'blocks') = 'array'
        AND jsonb_typeof(event->'stage') = 'string'
    ),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (team_id, run_id, capture_id, sequence),
    FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE,
    FOREIGN KEY (team_id, session_id) REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_context_trajectory_session
    ON public.context_trajectory_events (team_id, bot_id, session_id, id DESC);

ALTER TABLE public.context_trajectory_contents ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.context_trajectory_contents FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS context_trajectory_contents_team_select ON public.context_trajectory_contents;
CREATE POLICY context_trajectory_contents_team_select ON public.context_trajectory_contents
    FOR SELECT USING (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS context_trajectory_contents_team_insert ON public.context_trajectory_contents;
CREATE POLICY context_trajectory_contents_team_insert ON public.context_trajectory_contents
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS context_trajectory_contents_team_update ON public.context_trajectory_contents;
CREATE POLICY context_trajectory_contents_team_update ON public.context_trajectory_contents
    FOR UPDATE USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS context_trajectory_contents_team_delete ON public.context_trajectory_contents;
CREATE POLICY context_trajectory_contents_team_delete ON public.context_trajectory_contents
    FOR DELETE USING (team_id = public.memoh_current_team_id());

ALTER TABLE public.context_trajectory_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.context_trajectory_events FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS context_trajectory_events_team_select ON public.context_trajectory_events;
CREATE POLICY context_trajectory_events_team_select ON public.context_trajectory_events
    FOR SELECT USING (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS context_trajectory_events_team_insert ON public.context_trajectory_events;
CREATE POLICY context_trajectory_events_team_insert ON public.context_trajectory_events
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS context_trajectory_events_team_update ON public.context_trajectory_events;
CREATE POLICY context_trajectory_events_team_update ON public.context_trajectory_events
    FOR UPDATE USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS context_trajectory_events_team_delete ON public.context_trajectory_events;
CREATE POLICY context_trajectory_events_team_delete ON public.context_trajectory_events
    FOR DELETE USING (team_id = public.memoh_current_team_id());
