-- 0149_apps
-- Apps are the unit users install from the Supermarket. An installation
-- records the immutable release revision it materialized; its references to
-- workspace dependencies and Connect-It connections keep those resources
-- shared across Apps instead of duplicated per App.
-- The Skill-only installation table is replaced without migrating rows: no
-- release shipped it.

DROP TABLE IF EXISTS public.bot_skill_package_installations;

CREATE TABLE IF NOT EXISTS public.bot_app_installations (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id             UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                    REFERENCES public.teams(id) ON DELETE RESTRICT,
    bot_id              UUID        NOT NULL,
    workspace_target_id TEXT        NOT NULL,
    registry_id         TEXT        NOT NULL,
    app_id              TEXT        NOT NULL,
    revision            TEXT        NOT NULL,
    version             TEXT        NOT NULL DEFAULT '',
    status              TEXT        NOT NULL DEFAULT 'installing',
    reason              TEXT        NOT NULL DEFAULT 'user',
    available_revision  TEXT        NOT NULL DEFAULT '',
    available_version   TEXT        NOT NULL DEFAULT '',
    last_checked_at     TIMESTAMPTZ,
    last_error          TEXT        NOT NULL DEFAULT '',
    -- The release document the installation materialized, so the App
    -- view does not depend on the Supermarket being reachable.
    release             BYTEA       NOT NULL DEFAULT ''::bytea,
    installed_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT memoh_team_key_418800cc0ea5 UNIQUE (team_id, id),
    CONSTRAINT bot_app_installations_identity_key
        UNIQUE (team_id, bot_id, workspace_target_id, registry_id, app_id),
    CONSTRAINT bot_app_installations_revision_check
        CHECK (revision ~ '^[0-9a-f]{64}$'),
    CONSTRAINT bot_app_installations_available_revision_check
        CHECK (available_revision = '' OR available_revision ~ '^[0-9a-f]{64}$'),
    CONSTRAINT bot_app_installations_registry_id_check
        CHECK (registry_id <> ''),
    CONSTRAINT bot_app_installations_app_id_check
        CHECK (app_id <> ''),
    CONSTRAINT bot_app_installations_status_check
        CHECK (status IN ('installed', 'partial', 'installing', 'updating', 'removing', 'failed')),
    CONSTRAINT bot_app_installations_reason_check
        CHECK (reason IN ('user', 'required')),
    CONSTRAINT bot_app_installations_release_check
        CHECK (octet_length(release) <= 8388608)
);

-- bots is under FORCE ROW LEVEL SECURITY; add the reference NOT VALID so the
-- constraint is never validated through the policy-scoped scan.
ALTER TABLE public.bot_app_installations
    DROP CONSTRAINT IF EXISTS bot_app_installations_bot_id_fkey;
ALTER TABLE public.bot_app_installations
    ADD CONSTRAINT bot_app_installations_bot_id_fkey
    FOREIGN KEY (team_id, bot_id)
    REFERENCES public.bots(team_id, id) ON DELETE CASCADE
    NOT VALID;

ALTER TABLE public.bot_app_installations ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_app_installations FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS bot_app_installations_team_select ON public.bot_app_installations;
DROP POLICY IF EXISTS bot_app_installations_team_insert ON public.bot_app_installations;
DROP POLICY IF EXISTS bot_app_installations_team_update ON public.bot_app_installations;
DROP POLICY IF EXISTS bot_app_installations_team_delete ON public.bot_app_installations;

CREATE POLICY bot_app_installations_team_select ON public.bot_app_installations
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_app_installations_team_insert ON public.bot_app_installations
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_app_installations_team_update ON public.bot_app_installations
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_app_installations_team_delete ON public.bot_app_installations
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- An App installation references the workspace dependencies it needs.
-- The dependency itself stays one record per (bot, target, dependency) in
-- bot_dependency_installations; a dependency is removed only when its last
-- referencing installation goes away.
CREATE TABLE IF NOT EXISTS public.bot_app_dependency_refs (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id         UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                REFERENCES public.teams(id) ON DELETE RESTRICT,
    installation_id UUID        NOT NULL,
    dependency_id   TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT memoh_team_key_836f95d79b1b UNIQUE (team_id, id),
    CONSTRAINT bot_app_dependency_refs_identity_key
        UNIQUE (team_id, installation_id, dependency_id),
    CONSTRAINT bot_app_dependency_refs_installation_id_fkey
        FOREIGN KEY (team_id, installation_id)
        REFERENCES public.bot_app_installations(team_id, id) ON DELETE CASCADE,
    CONSTRAINT bot_app_dependency_refs_dependency_id_check
        CHECK (dependency_id <> '')
);

ALTER TABLE public.bot_app_dependency_refs ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_app_dependency_refs FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS bot_app_dependency_refs_team_select ON public.bot_app_dependency_refs;
DROP POLICY IF EXISTS bot_app_dependency_refs_team_insert ON public.bot_app_dependency_refs;
DROP POLICY IF EXISTS bot_app_dependency_refs_team_update ON public.bot_app_dependency_refs;
DROP POLICY IF EXISTS bot_app_dependency_refs_team_delete ON public.bot_app_dependency_refs;

CREATE POLICY bot_app_dependency_refs_team_select ON public.bot_app_dependency_refs
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_app_dependency_refs_team_insert ON public.bot_app_dependency_refs
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_app_dependency_refs_team_update ON public.bot_app_dependency_refs
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_app_dependency_refs_team_delete ON public.bot_app_dependency_refs
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- An App installation references the Connect-It connector types it uses.
-- connection_id is empty until the user authorizes one; the connection itself
-- is the bot-level binding in public.connectors and is shared by every
-- installation that references it.
CREATE TABLE IF NOT EXISTS public.bot_app_connector_refs (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id         UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                REFERENCES public.teams(id) ON DELETE RESTRICT,
    installation_id UUID        NOT NULL,
    connector_type  TEXT        NOT NULL,
    connection_id   TEXT        NOT NULL DEFAULT '',
    required        BOOLEAN     NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT memoh_team_key_56748bb759c8 UNIQUE (team_id, id),
    CONSTRAINT bot_app_connector_refs_identity_key
        UNIQUE (team_id, installation_id, connector_type),
    CONSTRAINT bot_app_connector_refs_installation_id_fkey
        FOREIGN KEY (team_id, installation_id)
        REFERENCES public.bot_app_installations(team_id, id) ON DELETE CASCADE,
    CONSTRAINT bot_app_connector_refs_connector_type_check
        CHECK (connector_type <> '')
);

ALTER TABLE public.bot_app_connector_refs ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_app_connector_refs FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS bot_app_connector_refs_team_select ON public.bot_app_connector_refs;
DROP POLICY IF EXISTS bot_app_connector_refs_team_insert ON public.bot_app_connector_refs;
DROP POLICY IF EXISTS bot_app_connector_refs_team_update ON public.bot_app_connector_refs;
DROP POLICY IF EXISTS bot_app_connector_refs_team_delete ON public.bot_app_connector_refs;

CREATE POLICY bot_app_connector_refs_team_select ON public.bot_app_connector_refs
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_app_connector_refs_team_insert ON public.bot_app_connector_refs
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_app_connector_refs_team_update ON public.bot_app_connector_refs
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_app_connector_refs_team_delete ON public.bot_app_connector_refs
    FOR DELETE USING (team_id = public.memoh_current_team_id());
