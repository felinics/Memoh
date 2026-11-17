-- 0174_drop_team_dependency_catalog
-- Drop the per-team dependency definition and catalog tables. Since 0173 every
-- Server reads template.workspace_dependency_*; this migration must be deployed
-- only after no Server of the previous release is running.

-- The migration owner reads every team's rows while FORCE RLS is lifted; the
-- policies call memoh_current_team_id(), which raises without a team context.
ALTER TABLE public.workspace_dependency_definitions NO FORCE ROW LEVEL SECURITY;

-- Servers still on the previous release wrote definitions here during the
-- rolling deploy. Copy the verified ones with the same check and dedupe as
-- 0173: the release document hashes to its revision and the archive hashes to
-- the digest that document declares. CASE keeps the JSON parse behind the
-- release hash check. The catalog is not merged again; the refresher rebuilds
-- it from the registry.
INSERT INTO template.workspace_dependency_definitions (
    source_url, registry_id, dependency_id, revision,
    release_bytes, artifact_bytes, icon_digest, created_at
)
SELECT DISTINCT ON (source_url, registry_id, dependency_id, revision)
    source_url, registry_id, dependency_id, revision,
    release_bytes, artifact_bytes, icon_digest,
    min(created_at) OVER (PARTITION BY source_url, registry_id, dependency_id, revision)
FROM public.workspace_dependency_definitions
WHERE CASE
    WHEN encode(sha256(release_bytes), 'hex') = revision
    THEN encode(sha256(artifact_bytes), 'hex')
         = convert_from(release_bytes, 'UTF8')::jsonb #>> '{artifact,digest}'
    ELSE false
END
ORDER BY source_url, registry_id, dependency_id, revision, created_at
ON CONFLICT DO NOTHING;

-- Policies and the icon index are dropped with the tables.
DROP TABLE public.workspace_dependency_definitions;
DROP TABLE public.workspace_dependency_catalogs;
