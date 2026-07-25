-- 0116_subagent_configs
-- Persist the model selection and optional parent-message snapshot for managed subagents.

CREATE TABLE IF NOT EXISTS public.subagent_configs (
    team_id         UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                REFERENCES public.teams(id) ON DELETE RESTRICT,
    session_id      UUID        PRIMARY KEY,
    model_uuid      UUID,
    model_id        TEXT        NOT NULL,
    provider_name   TEXT        NOT NULL,
    forked          BOOLEAN     NOT NULL DEFAULT false,
    parent_messages JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT subagent_configs_team_session_key UNIQUE (team_id, session_id),
    CONSTRAINT subagent_configs_session_id_fkey
        FOREIGN KEY (team_id, session_id)
        REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE,
    CONSTRAINT subagent_configs_model_uuid_fkey
        FOREIGN KEY (team_id, model_uuid)
        REFERENCES public.models(team_id, id) ON DELETE SET NULL (model_uuid),
    CONSTRAINT subagent_configs_fork_snapshot_check CHECK (
        (forked AND parent_messages IS NOT NULL AND jsonb_typeof(parent_messages) = 'array')
        OR (NOT forked AND parent_messages IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_subagent_configs_team_model
    ON public.subagent_configs (team_id, model_uuid);

ALTER TABLE public.subagent_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.subagent_configs FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS subagent_configs_team_select ON public.subagent_configs;
DROP POLICY IF EXISTS subagent_configs_team_insert ON public.subagent_configs;
DROP POLICY IF EXISTS subagent_configs_team_update ON public.subagent_configs;
DROP POLICY IF EXISTS subagent_configs_team_delete ON public.subagent_configs;

CREATE POLICY subagent_configs_team_select ON public.subagent_configs
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY subagent_configs_team_insert ON public.subagent_configs
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY subagent_configs_team_update ON public.subagent_configs
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY subagent_configs_team_delete ON public.subagent_configs
    FOR DELETE USING (team_id = public.memoh_current_team_id());
