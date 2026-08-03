-- 0129_add_agent_step_commits
-- Record complete native-agent steps so their history writes are idempotent.

CREATE TABLE IF NOT EXISTS public.agent_step_commits (
    team_id       UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                              REFERENCES public.teams(id) ON DELETE RESTRICT,
    run_id        UUID        NOT NULL,
    step_index    BIGINT      NOT NULL CHECK (step_index >= 0),
    message_count INTEGER     NOT NULL CHECK (message_count > 0),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_step_commits_pkey PRIMARY KEY (team_id, run_id, step_index),
    CONSTRAINT agent_step_commits_run_fkey
        FOREIGN KEY (team_id, run_id)
        REFERENCES public.session_runs(team_id, run_id) ON DELETE CASCADE
);

ALTER TABLE public.agent_step_commits ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agent_step_commits FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS agent_step_commits_team_select ON public.agent_step_commits;
DROP POLICY IF EXISTS agent_step_commits_team_insert ON public.agent_step_commits;
DROP POLICY IF EXISTS agent_step_commits_team_update ON public.agent_step_commits;
DROP POLICY IF EXISTS agent_step_commits_team_delete ON public.agent_step_commits;

CREATE POLICY agent_step_commits_team_select ON public.agent_step_commits
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY agent_step_commits_team_insert ON public.agent_step_commits
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY agent_step_commits_team_update ON public.agent_step_commits
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY agent_step_commits_team_delete ON public.agent_step_commits
    FOR DELETE USING (team_id = public.memoh_current_team_id());
