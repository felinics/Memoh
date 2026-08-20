-- 0141_bot_agents
-- Persist user-added Bot Agents and bind defaults, sessions, and schedules to them.

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

-- PostgreSQL validates replacement constraints by scanning their tables. A
-- non-superuser migration owner is subject to FORCE RLS during those scans,
-- so suspend it before rebuilding constraints and backfilling across teams.
-- The migration runs transactionally and restores every table at the end.
ALTER TABLE public.bot_agents NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_agents DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.schedule NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.schedule DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots DISABLE ROW LEVEL SECURITY;

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
    ),
    DROP CONSTRAINT IF EXISTS schedule_acp_fields_check,
    ADD CONSTRAINT schedule_acp_fields_check CHECK (
        run_target <> 'new_session'
        OR (runtime_type = 'acp_agent' AND acp_agent_id IS NOT NULL AND model_id IS NULL)
        OR (COALESCE(runtime_type, 'model') = 'model' AND bot_agent_id IS NULL AND acp_agent_id IS NULL AND acp_model_id IS NULL)
    );

ALTER TABLE public.bots
    DROP CONSTRAINT IF EXISTS bots_default_bot_agent_id_fkey,
    ADD CONSTRAINT bots_default_bot_agent_id_fkey
        FOREIGN KEY (team_id, id, default_bot_agent_id)
        REFERENCES public.bot_agents(team_id, bot_id, id)
        ON DELETE SET NULL (default_bot_agent_id);

ALTER TABLE public.bot_sessions
    DROP CONSTRAINT IF EXISTS bot_sessions_bot_agent_id_fkey,
    ADD CONSTRAINT bot_sessions_bot_agent_id_fkey
        FOREIGN KEY (team_id, bot_id, bot_agent_id)
        REFERENCES public.bot_agents(team_id, bot_id, id)
        ON DELETE SET NULL (bot_agent_id);

ALTER TABLE public.schedule
    DROP CONSTRAINT IF EXISTS schedule_bot_agent_id_fkey,
    ADD CONSTRAINT schedule_bot_agent_id_fkey
        FOREIGN KEY (team_id, bot_id, bot_agent_id)
        REFERENCES public.bot_agents(team_id, bot_id, id)
        ON DELETE SET NULL (bot_agent_id);

CREATE INDEX IF NOT EXISTS idx_bot_sessions_bot_agent
    ON public.bot_sessions (team_id, bot_agent_id)
    WHERE bot_agent_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_schedule_bot_agent
    ON public.schedule (team_id, bot_agent_id)
    WHERE bot_agent_id IS NOT NULL;

WITH raw_candidates AS (
    SELECT
        bot.team_id,
        bot.id AS bot_id,
        lower(btrim(agent.key)) AS provider,
        lower(COALESCE(agent.value->>'enabled', 'false')) = 'true' AS enabled
    FROM public.bots AS bot
    CROSS JOIN LATERAL jsonb_each(
        CASE
            WHEN jsonb_typeof(bot.metadata->'acp'->'agents') = 'object'
                THEN bot.metadata->'acp'->'agents'
            ELSE '{}'::jsonb
        END
    ) AS agent

    UNION ALL

    SELECT
        bot.team_id,
        bot.id,
        lower(btrim(bot.chat_acp_agent_id)),
        false
    FROM public.bots AS bot
    WHERE btrim(COALESCE(bot.chat_acp_agent_id, '')) <> ''

    UNION ALL

    SELECT
        session.team_id,
        session.bot_id,
        lower(btrim(COALESCE(
            session.runtime_metadata->>'acp_agent_id',
            session.metadata->>'acp_agent_id'
        ))),
        false
    FROM public.bot_sessions AS session
    WHERE session.runtime_type = 'acp_agent'
       OR session.type = 'acp_agent'

    UNION ALL

    SELECT
        schedule.team_id,
        schedule.bot_id,
        lower(btrim(schedule.acp_agent_id)),
        false
    FROM public.schedule AS schedule
    WHERE btrim(COALESCE(schedule.acp_agent_id, '')) <> ''
),
candidates AS (
    SELECT team_id, bot_id, provider, bool_or(enabled) AS enabled
    FROM raw_candidates
    WHERE btrim(COALESCE(provider, '')) <> ''
    GROUP BY team_id, bot_id, provider
)
INSERT INTO public.bot_agents (team_id, bot_id, name, runtime, enabled, metadata)
SELECT
    candidate.team_id,
    candidate.bot_id,
    CASE candidate.provider
        WHEN 'codex' THEN 'Codex'
        WHEN 'claude-code' THEN 'Claude Code'
        WHEN 'hermes' THEN 'Hermes'
        ELSE candidate.provider
    END,
    'acp',
    candidate.enabled,
    jsonb_build_object('provider', candidate.provider)
FROM candidates AS candidate
ON CONFLICT (team_id, bot_id, lower(btrim(name))) WHERE deleted_at IS NULL
DO NOTHING;

UPDATE public.bots AS bot
SET default_bot_agent_id = agent.id
FROM public.bot_agents AS agent
WHERE bot.team_id = agent.team_id
  AND bot.id = agent.bot_id
  AND bot.chat_runtime = 'acp_agent'
  AND agent.runtime = 'acp'
  AND agent.metadata->>'provider' = lower(btrim(bot.chat_acp_agent_id))
  AND agent.enabled
  AND agent.deleted_at IS NULL;

UPDATE public.bot_sessions AS session
SET bot_agent_id = agent.id
FROM public.bot_agents AS agent
WHERE session.team_id = agent.team_id
  AND session.bot_id = agent.bot_id
  AND agent.runtime = 'acp'
  AND agent.metadata->>'provider' = lower(btrim(COALESCE(
      session.runtime_metadata->>'acp_agent_id',
      session.metadata->>'acp_agent_id'
  )))
  AND (session.runtime_type = 'acp_agent' OR session.type = 'acp_agent');

UPDATE public.schedule AS schedule
SET bot_agent_id = agent.id
FROM public.bot_agents AS agent
WHERE schedule.team_id = agent.team_id
  AND schedule.bot_id = agent.bot_id
  AND agent.runtime = 'acp'
  AND agent.metadata->>'provider' = lower(btrim(schedule.acp_agent_id));

ALTER TABLE public.bot_agents ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_agents FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE public.schedule ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.schedule FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;
