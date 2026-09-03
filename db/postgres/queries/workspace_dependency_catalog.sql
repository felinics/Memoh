-- name: GetWorkspaceDependencyDefinition :one
-- Renew the retention lease before a caller prepares or recovers an operation.
-- A concurrent prune either observes the renewed lease or wins first and makes
-- this lookup fail, so an accepted preview never loses its definition to GC.
UPDATE workspace_dependency_definitions SET last_accessed_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND source_url = $1 AND registry_id = $2 AND dependency_id = $3 AND revision = $4
RETURNING source_url, registry_id, dependency_id, revision, release_bytes, artifact_bytes;

-- name: CacheWorkspaceDependencyDefinition :execrows
INSERT INTO workspace_dependency_definitions (source_url, registry_id, dependency_id, revision, release_bytes, artifact_bytes, icon_digest)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (team_id, source_url, registry_id, dependency_id, revision) DO NOTHING;

-- name: GetWorkspaceDependencyCatalog :one
SELECT team_id, source_url, catalog_bytes, generation, fetched_at
FROM workspace_dependency_catalogs
WHERE team_id = public.memoh_current_team_id() AND source_url = $1;

-- name: CacheWorkspaceDependencyCatalog :execrows
INSERT INTO workspace_dependency_catalogs (source_url, catalog_bytes, generation, fetched_at)
VALUES (sqlc.arg(source_url), sqlc.arg(catalog_bytes), 1, now())
ON CONFLICT (team_id, source_url) DO UPDATE
SET catalog_bytes = EXCLUDED.catalog_bytes, generation = workspace_dependency_catalogs.generation + 1, fetched_at = now()
WHERE workspace_dependency_catalogs.generation = sqlc.arg(expected_generation)::bigint;

-- name: FindWorkspaceDependencyIcon :one
SELECT source_url, registry_id, dependency_id, revision, release_bytes, artifact_bytes
FROM workspace_dependency_definitions
WHERE team_id = public.memoh_current_team_id() AND source_url = $1
  AND icon_digest = sqlc.arg(digest)::text
LIMIT 1;

-- name: PruneWorkspaceDependencyDefinitions :execrows
-- Keep current/retired catalog publications and every revision of dependencies
-- with installation intent: rollback state can refer to an earlier publication
-- from another source after the operator switches registries.
-- The grace period also protects previews and concurrently prepared operations.
WITH catalog_references AS MATERIALIZED (
  SELECT entry ->> 'id' AS dependency_id, entry ->> 'revision' AS revision
  FROM workspace_dependency_catalogs AS catalog,
    jsonb_array_elements(convert_from(catalog.catalog_bytes, 'UTF8')::jsonb -> 'entries') AS entry
  WHERE catalog.team_id = public.memoh_current_team_id()
    AND catalog.source_url = sqlc.arg(source_url)
)
DELETE FROM workspace_dependency_definitions AS definition
WHERE definition.team_id = public.memoh_current_team_id()
  AND definition.source_url = sqlc.arg(source_url)
  AND definition.last_accessed_at < now() - interval '30 days'
  AND NOT EXISTS (
    SELECT 1 FROM catalog_references AS reference
    WHERE reference.dependency_id = definition.dependency_id AND reference.revision = definition.revision
  )
  AND NOT EXISTS (
    SELECT 1 FROM bot_dependency_installations AS installation
    WHERE installation.team_id = definition.team_id
      AND installation.dependency_id = definition.dependency_id
  );
