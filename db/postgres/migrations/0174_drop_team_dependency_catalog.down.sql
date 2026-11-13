-- 0174_drop_team_dependency_catalog
-- Recreate the per-team dependency definition and catalog tables as they were
-- before this migration, empty. Rolled-back data is not restored: the
-- definitions were copied into template.workspace_dependency_definitions and
-- the catalog is rebuilt by the refresher.

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

