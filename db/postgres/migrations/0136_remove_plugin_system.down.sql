-- 0136_remove_plugin_system
-- Restore only the retired Plugin schema for a migration rollback. Plugin
-- installations, resources, and managed MCP connections deleted by the up
-- migration cannot be recovered.

CREATE TABLE IF NOT EXISTS public.bot_plugin_installations (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id      UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                             REFERENCES public.teams(id) ON DELETE RESTRICT,
    bot_id       UUID        NOT NULL,
    plugin_id    TEXT        NOT NULL,
    plugin_name  TEXT        NOT NULL DEFAULT '',
    version      TEXT        NOT NULL DEFAULT '',
    status       TEXT        NOT NULL DEFAULT 'ready',
    enabled      BOOLEAN     NOT NULL DEFAULT true,
    config       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    metadata     JSONB       NOT NULL DEFAULT '{}'::jsonb,
    manifest     JSONB       NOT NULL DEFAULT '{}'::jsonb,
    installed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT memoh_team_key_bot_plugin_installations UNIQUE (team_id, id),
    CONSTRAINT bot_plugin_installations_unique UNIQUE (team_id, bot_id, plugin_id),
    CONSTRAINT bot_plugin_installations_bot_id_fkey
        FOREIGN KEY (team_id, bot_id)
        REFERENCES public.bots(team_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_bot_plugin_installations_bot_id
    ON public.bot_plugin_installations(team_id, bot_id);
CREATE INDEX IF NOT EXISTS idx_bot_plugin_installations_plugin_id
    ON public.bot_plugin_installations(team_id, plugin_id);

ALTER TABLE public.bot_plugin_installations ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_plugin_installations FORCE ROW LEVEL SECURITY;

CREATE POLICY bot_plugin_installations_team_select ON public.bot_plugin_installations
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_plugin_installations_team_insert ON public.bot_plugin_installations
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_plugin_installations_team_update ON public.bot_plugin_installations
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_plugin_installations_team_delete ON public.bot_plugin_installations
    FOR DELETE USING (team_id = public.memoh_current_team_id());

ALTER TABLE public.mcp_connections
    ADD COLUMN managed_by_plugin_installation_id UUID,
    ADD COLUMN managed_resource_key TEXT NOT NULL DEFAULT '',
    ADD COLUMN visible BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD CONSTRAINT mcp_connections_managed_plugin_fkey
        FOREIGN KEY (team_id, managed_by_plugin_installation_id)
        REFERENCES public.bot_plugin_installations(team_id, id)
        ON DELETE SET NULL (managed_by_plugin_installation_id);

CREATE INDEX IF NOT EXISTS idx_mcp_connections_plugin_installation_id
    ON public.mcp_connections(team_id, managed_by_plugin_installation_id);

CREATE TABLE IF NOT EXISTS public.bot_plugin_resources (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id         UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                REFERENCES public.teams(id) ON DELETE RESTRICT,
    installation_id UUID        NOT NULL,
    resource_type   TEXT        NOT NULL,
    resource_key    TEXT        NOT NULL,
    resource_id     TEXT        NOT NULL DEFAULT '',
    status          TEXT        NOT NULL DEFAULT 'active',
    metadata        JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT memoh_team_key_bot_plugin_resources UNIQUE (team_id, id),
    CONSTRAINT bot_plugin_resources_unique
        UNIQUE (team_id, installation_id, resource_type, resource_key),
    CONSTRAINT bot_plugin_resources_installation_id_fkey
        FOREIGN KEY (team_id, installation_id)
        REFERENCES public.bot_plugin_installations(team_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_bot_plugin_resources_installation_id
    ON public.bot_plugin_resources(team_id, installation_id);
CREATE INDEX IF NOT EXISTS idx_bot_plugin_resources_resource
    ON public.bot_plugin_resources(team_id, resource_type, resource_id);

ALTER TABLE public.bot_plugin_resources ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_plugin_resources FORCE ROW LEVEL SECURITY;

CREATE POLICY bot_plugin_resources_team_select ON public.bot_plugin_resources
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_plugin_resources_team_insert ON public.bot_plugin_resources
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_plugin_resources_team_update ON public.bot_plugin_resources
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_plugin_resources_team_delete ON public.bot_plugin_resources
    FOR DELETE USING (team_id = public.memoh_current_team_id());
