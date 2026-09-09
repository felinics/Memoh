-- 0148_bot_dependency_installations
-- Record per-bot, per-workspace-target dependency installation intent for the
-- Workspace dependency manager.
-- Rows express what the user asked for; discovery corrects status/source and
-- never deletes a record.

CREATE TABLE IF NOT EXISTS public.bot_dependency_installations (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id             UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                    REFERENCES public.teams(id) ON DELETE RESTRICT,
    bot_id              UUID        NOT NULL,
    workspace_target_id TEXT        NOT NULL,
    dependency_id       TEXT        NOT NULL,
    source              TEXT        NOT NULL,
    status              TEXT        NOT NULL,
    installed_version   TEXT        NOT NULL DEFAULT '',
    latest_version      TEXT        NOT NULL DEFAULT '',
    last_checked_at     TIMESTAMPTZ,
    last_error          TEXT        NOT NULL DEFAULT '',
    manifest_digest     TEXT        NOT NULL DEFAULT '',
    source_url          TEXT        NOT NULL DEFAULT '',
    registry_id         TEXT        NOT NULL DEFAULT '',
    definition_revision TEXT        NOT NULL DEFAULT '',
    operation_id        TEXT        NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT memoh_team_key_07a3be5666c2 UNIQUE (team_id, id),
    CONSTRAINT bot_dependency_installations_identity_key
        UNIQUE (team_id, bot_id, workspace_target_id, dependency_id),
    CONSTRAINT bot_dependency_installations_dependency_id_check
        CHECK (dependency_id <> ''),
    CONSTRAINT bot_dependency_installations_source_check
        CHECK (source IN ('image', 'managed')),
    CONSTRAINT bot_dependency_installations_status_check
        CHECK (status IN ('installed', 'installing', 'updating', 'removing', 'missing', 'failed'))
);

-- bots is under FORCE ROW LEVEL SECURITY; add the reference NOT VALID so the
-- constraint is never validated through the policy-scoped scan.
ALTER TABLE public.bot_dependency_installations
    DROP CONSTRAINT IF EXISTS bot_dependency_installations_bot_id_fkey;
ALTER TABLE public.bot_dependency_installations
    ADD CONSTRAINT bot_dependency_installations_bot_id_fkey
    FOREIGN KEY (team_id, bot_id)
    REFERENCES public.bots(team_id, id) ON DELETE CASCADE
    NOT VALID;

-- The operation token fences terminal writes from other Server instances.
ALTER TABLE public.bot_dependency_installations
    ADD COLUMN IF NOT EXISTS operation_id TEXT NOT NULL DEFAULT '';
ALTER TABLE public.bot_dependency_installations
    DROP CONSTRAINT IF EXISTS bot_dependency_installations_operation_id_check;
ALTER TABLE public.bot_dependency_installations
    ADD CONSTRAINT bot_dependency_installations_operation_id_check
    CHECK (operation_id = '' OR operation_id ~ '^[a-f0-9]{32}$');

-- The identity unique key also serves target-scoped installation lookups.
DROP INDEX IF EXISTS public.idx_bot_dependency_installations_bot;

ALTER TABLE public.bot_dependency_installations ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_dependency_installations FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS bot_dependency_installations_team_select ON public.bot_dependency_installations;
DROP POLICY IF EXISTS bot_dependency_installations_team_insert ON public.bot_dependency_installations;
DROP POLICY IF EXISTS bot_dependency_installations_team_update ON public.bot_dependency_installations;
DROP POLICY IF EXISTS bot_dependency_installations_team_delete ON public.bot_dependency_installations;

CREATE POLICY bot_dependency_installations_team_select ON public.bot_dependency_installations
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_dependency_installations_team_insert ON public.bot_dependency_installations
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_dependency_installations_team_update ON public.bot_dependency_installations
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_dependency_installations_team_delete ON public.bot_dependency_installations
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- Immutable dependency definitions and the independently refreshed catalog.
-- Entries are shared only within a team and one configured Supermarket origin.
CREATE TABLE IF NOT EXISTS public.workspace_dependency_definitions (
    team_id UUID NOT NULL DEFAULT public.memoh_current_team_id() REFERENCES public.teams(id) ON DELETE RESTRICT,
    source_url TEXT NOT NULL,
    registry_id TEXT NOT NULL CHECK (registry_id = 'memoh'),
    dependency_id TEXT NOT NULL,
    revision TEXT NOT NULL CHECK (revision ~ '^[a-f0-9]{64}$'),
    release_bytes BYTEA NOT NULL CHECK (octet_length(release_bytes) BETWEEN 1 AND 1048576),
    artifact_bytes BYTEA NOT NULL CHECK (octet_length(artifact_bytes) BETWEEN 1 AND 1048576),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    icon_digest TEXT NOT NULL DEFAULT '',
    last_accessed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, source_url, registry_id, dependency_id, revision)
);

ALTER TABLE public.workspace_dependency_definitions
    ADD COLUMN IF NOT EXISTS icon_digest TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_accessed_at TIMESTAMPTZ NOT NULL DEFAULT now();
-- Upgrade cached releases written before the indexed icon digest was added.
-- The migration owner can backfill every team while FORCE RLS is lifted.
ALTER TABLE public.workspace_dependency_definitions NO FORCE ROW LEVEL SECURITY;
UPDATE public.workspace_dependency_definitions
SET icon_digest = COALESCE(convert_from(release_bytes, 'UTF8')::jsonb -> 'icon' ->> 'digest', '')
WHERE icon_digest = '';
CREATE INDEX IF NOT EXISTS idx_workspace_dependency_definitions_icon
    ON public.workspace_dependency_definitions (team_id, source_url, icon_digest);

CREATE TABLE IF NOT EXISTS public.workspace_dependency_catalogs (
    team_id UUID NOT NULL DEFAULT public.memoh_current_team_id() REFERENCES public.teams(id) ON DELETE RESTRICT,
    source_url TEXT NOT NULL,
    catalog_bytes BYTEA NOT NULL CHECK (octet_length(catalog_bytes) BETWEEN 1 AND 4194304),
    generation BIGINT NOT NULL DEFAULT 1 CHECK (generation > 0),
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, source_url)
);

ALTER TABLE public.workspace_dependency_definitions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspace_dependency_definitions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS workspace_dependency_definitions_team_select ON public.workspace_dependency_definitions;
CREATE POLICY workspace_dependency_definitions_team_select ON public.workspace_dependency_definitions
    FOR SELECT USING (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS workspace_dependency_definitions_team_insert ON public.workspace_dependency_definitions;
CREATE POLICY workspace_dependency_definitions_team_insert ON public.workspace_dependency_definitions
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS workspace_dependency_definitions_team_update ON public.workspace_dependency_definitions;
CREATE POLICY workspace_dependency_definitions_team_update ON public.workspace_dependency_definitions
    FOR UPDATE USING (team_id = public.memoh_current_team_id()) WITH CHECK (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS workspace_dependency_definitions_team_delete ON public.workspace_dependency_definitions;
CREATE POLICY workspace_dependency_definitions_team_delete ON public.workspace_dependency_definitions
    FOR DELETE USING (team_id = public.memoh_current_team_id());

ALTER TABLE public.workspace_dependency_catalogs ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspace_dependency_catalogs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS workspace_dependency_catalogs_team_select ON public.workspace_dependency_catalogs;
CREATE POLICY workspace_dependency_catalogs_team_select ON public.workspace_dependency_catalogs
    FOR SELECT USING (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS workspace_dependency_catalogs_team_insert ON public.workspace_dependency_catalogs;
CREATE POLICY workspace_dependency_catalogs_team_insert ON public.workspace_dependency_catalogs
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS workspace_dependency_catalogs_team_update ON public.workspace_dependency_catalogs;
CREATE POLICY workspace_dependency_catalogs_team_update ON public.workspace_dependency_catalogs
    FOR UPDATE USING (team_id = public.memoh_current_team_id()) WITH CHECK (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS workspace_dependency_catalogs_team_delete ON public.workspace_dependency_catalogs;
CREATE POLICY workspace_dependency_catalogs_team_delete ON public.workspace_dependency_catalogs
    FOR DELETE USING (team_id = public.memoh_current_team_id());
