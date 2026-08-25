-- 0141_bot_agents
-- Persist user-added Bot Agents and bind defaults, sessions, and schedules to them.
-- Legacy ACP data remains in its existing columns/metadata; this migration does
-- not create Agent rows or populate the new nullable bindings.

CREATE TABLE IF NOT EXISTS public.bot_agents (
    team_id    UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                            REFERENCES public.teams(id) ON DELETE RESTRICT,
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    bot_id     UUID        NOT NULL,
    name       TEXT        NOT NULL,
    runtime    TEXT        NOT NULL,
    enabled    BOOLEAN     NOT NULL DEFAULT true,
    metadata   JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT bot_agents_team_id_key UNIQUE (team_id, id),
    CONSTRAINT bot_agents_team_bot_id_key UNIQUE (team_id, bot_id, id),
    CONSTRAINT bot_agents_bot_id_fkey
        FOREIGN KEY (team_id, bot_id)
        REFERENCES public.bots(team_id, id) ON DELETE CASCADE,
    CONSTRAINT bot_agents_name_check CHECK (btrim(name) <> ''),
    CONSTRAINT bot_agents_runtime_check CHECK (btrim(runtime) <> ''),
    CONSTRAINT bot_agents_metadata_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE UNIQUE INDEX IF NOT EXISTS bot_agents_active_name_unique
    ON public.bot_agents (team_id, bot_id, lower(btrim(name)))
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_bot_agents_bot_active
    ON public.bot_agents (team_id, bot_id, created_at, id)
    WHERE deleted_at IS NULL;

DROP POLICY IF EXISTS bot_agents_team_select ON public.bot_agents;
DROP POLICY IF EXISTS bot_agents_team_insert ON public.bot_agents;
DROP POLICY IF EXISTS bot_agents_team_update ON public.bot_agents;
DROP POLICY IF EXISTS bot_agents_team_delete ON public.bot_agents;

CREATE POLICY bot_agents_team_select ON public.bot_agents
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_agents_team_insert ON public.bot_agents
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_agents_team_update ON public.bot_agents
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_agents_team_delete ON public.bot_agents
    FOR DELETE USING (team_id = public.memoh_current_team_id());

ALTER TABLE public.bots
    ADD COLUMN IF NOT EXISTS default_bot_agent_id UUID;

ALTER TABLE public.bot_sessions
    ADD COLUMN IF NOT EXISTS bot_agent_id UUID;

ALTER TABLE public.schedule
    ADD COLUMN IF NOT EXISTS bot_agent_id UUID;

ALTER TABLE public.schedule
    DROP CONSTRAINT IF EXISTS schedule_existing_session_check,
    ADD CONSTRAINT schedule_existing_session_check CHECK (
        run_target <> 'existing_session'
        OR (runtime_type IS NULL AND bot_agent_id IS NULL AND acp_agent_id IS NULL AND workdir_id IS NULL)
    ) NOT VALID,
    DROP CONSTRAINT IF EXISTS schedule_acp_fields_check,
    ADD CONSTRAINT schedule_acp_fields_check CHECK (
        run_target <> 'new_session'
        OR (runtime_type = 'acp_agent' AND acp_agent_id IS NOT NULL AND model_id IS NULL)
        OR (COALESCE(runtime_type, 'model') = 'model' AND bot_agent_id IS NULL AND acp_agent_id IS NULL AND acp_model_id IS NULL)
    ) NOT VALID;

ALTER TABLE public.bots
    DROP CONSTRAINT IF EXISTS bots_default_bot_agent_id_fkey,
    ADD CONSTRAINT bots_default_bot_agent_id_fkey
        FOREIGN KEY (team_id, id, default_bot_agent_id)
        REFERENCES public.bot_agents(team_id, bot_id, id)
        ON DELETE SET NULL (default_bot_agent_id)
        NOT VALID;

ALTER TABLE public.bot_sessions
    DROP CONSTRAINT IF EXISTS bot_sessions_bot_agent_id_fkey,
    ADD CONSTRAINT bot_sessions_bot_agent_id_fkey
        FOREIGN KEY (team_id, bot_id, bot_agent_id)
        REFERENCES public.bot_agents(team_id, bot_id, id)
        ON DELETE SET NULL (bot_agent_id)
        NOT VALID;

ALTER TABLE public.schedule
    DROP CONSTRAINT IF EXISTS schedule_bot_agent_id_fkey,
    ADD CONSTRAINT schedule_bot_agent_id_fkey
        FOREIGN KEY (team_id, bot_id, bot_agent_id)
        REFERENCES public.bot_agents(team_id, bot_id, id)
        ON DELETE SET NULL (bot_agent_id)
        NOT VALID;

CREATE INDEX IF NOT EXISTS idx_bot_sessions_bot_agent
    ON public.bot_sessions (team_id, bot_agent_id)
    WHERE bot_agent_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_schedule_bot_agent
    ON public.schedule (team_id, bot_agent_id)
    WHERE bot_agent_id IS NOT NULL;

ALTER TABLE public.bot_agents ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_agents FORCE ROW LEVEL SECURITY;
