-- Dependency definitions and catalogs are global: their content comes from the
-- server-level Supermarket source and is verified by digest on every read.

-- name: GetWorkspaceDependencyDefinition :one
SELECT source_url, registry_id, dependency_id, revision, release_bytes, artifact_bytes
FROM template.workspace_dependency_definitions
WHERE source_url = $1 AND registry_id = $2 AND dependency_id = $3 AND revision = $4;

-- name: CacheWorkspaceDependencyDefinition :execrows
INSERT INTO template.workspace_dependency_definitions (source_url, registry_id, dependency_id, revision, release_bytes, artifact_bytes, icon_digest)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (source_url, registry_id, dependency_id, revision) DO NOTHING;

-- name: GetWorkspaceDependencyCatalog :one
SELECT source_url, catalog_bytes, generation, fetched_at
FROM template.workspace_dependency_catalogs
WHERE source_url = $1;

-- name: CacheWorkspaceDependencyCatalog :execrows
INSERT INTO template.workspace_dependency_catalogs (source_url, catalog_bytes, generation, fetched_at)
VALUES (sqlc.arg(source_url), sqlc.arg(catalog_bytes), 1, now())
ON CONFLICT (source_url) DO UPDATE
SET catalog_bytes = EXCLUDED.catalog_bytes, generation = template.workspace_dependency_catalogs.generation + 1, fetched_at = now()
WHERE template.workspace_dependency_catalogs.generation = sqlc.arg(expected_generation)::bigint;

-- name: FindWorkspaceDependencyIcon :one
SELECT source_url, registry_id, dependency_id, revision, release_bytes, artifact_bytes
FROM template.workspace_dependency_definitions
WHERE source_url = $1
  AND icon_digest = sqlc.arg(digest)::text
LIMIT 1;
