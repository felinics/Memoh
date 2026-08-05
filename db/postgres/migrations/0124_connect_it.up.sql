-- 0124_connect_it
-- Add bot-scoped Connect-It connection bindings. A co-hosted Connect-It
-- deployment owns and migrates its own connect_it schema; Memoh stores only
-- the durable connection reference and whether the bot currently exposes it.

CREATE TABLE IF NOT EXISTS public.connectors (
    team_id       UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                              REFERENCES public.teams(id) ON DELETE RESTRICT,
    bot_id        UUID        NOT NULL,
    connection_id TEXT        NOT NULL,
    -- Durable per-bot tool namespace, allocated once at binding time. Tool
    -- names are derived from it, so it must never be recomputed from the
    -- current connection set: removing one connection must not rename (and
    -- silently reroute) another connection's tools.
    alias         TEXT        NOT NULL,
    enabled       BOOLEAN     NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT connectors_pkey PRIMARY KEY (team_id, bot_id, connection_id),
    CONSTRAINT connectors_team_connection_id_key UNIQUE (team_id, connection_id),
    CONSTRAINT connectors_team_bot_alias_key UNIQUE (team_id, bot_id, alias),
    CONSTRAINT connectors_bot_id_fkey
        FOREIGN KEY (team_id, bot_id)
        REFERENCES public.bots(team_id, id) ON DELETE CASCADE
);

ALTER TABLE public.connectors ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.connectors FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS connectors_team_select ON public.connectors;
DROP POLICY IF EXISTS connectors_team_insert ON public.connectors;
DROP POLICY IF EXISTS connectors_team_update ON public.connectors;
DROP POLICY IF EXISTS connectors_team_delete ON public.connectors;

CREATE POLICY connectors_team_select ON public.connectors
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY connectors_team_insert ON public.connectors
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY connectors_team_update ON public.connectors
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY connectors_team_delete ON public.connectors
    FOR DELETE USING (team_id = public.memoh_current_team_id());
