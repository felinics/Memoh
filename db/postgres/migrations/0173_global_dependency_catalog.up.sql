-- 0173_global_dependency_catalog
-- Store workspace dependency definitions and the cached catalog once per
-- Supermarket source instead of once per team. Their content is derived from
-- the server-level registry URL and verified by digest on every read, so it
-- belongs to no team. The per-team public tables stay in place for Server
-- instances still running the previous release during a rolling deploy.

CREATE SCHEMA IF NOT EXISTS template;

-- Definitions are append-only: rows are immutable publications keyed by their
-- content digest and are never deleted.
CREATE TABLE IF NOT EXISTS template.workspace_dependency_definitions (
    source_url TEXT NOT NULL,
    registry_id TEXT NOT NULL CHECK (registry_id = 'memoh'),
    dependency_id TEXT NOT NULL,
    revision TEXT NOT NULL CHECK (revision ~ '^[a-f0-9]{64}$'),
    release_bytes BYTEA NOT NULL CHECK (octet_length(release_bytes) BETWEEN 1 AND 1048576),
    artifact_bytes BYTEA NOT NULL CHECK (octet_length(artifact_bytes) BETWEEN 1 AND 1048576),
    icon_digest TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (source_url, registry_id, dependency_id, revision)
);

CREATE INDEX IF NOT EXISTS idx_template_workspace_dependency_definitions_icon
    ON template.workspace_dependency_definitions (source_url, icon_digest);

CREATE TABLE IF NOT EXISTS template.workspace_dependency_catalogs (
    source_url TEXT PRIMARY KEY,
    catalog_bytes BYTEA NOT NULL CHECK (octet_length(catalog_bytes) BETWEEN 1 AND 4194304),
    generation BIGINT NOT NULL DEFAULT 1 CHECK (generation > 0),
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

GRANT USAGE ON SCHEMA template TO CURRENT_USER;
GRANT SELECT, INSERT, UPDATE, DELETE ON
  template.workspace_dependency_definitions,
  template.workspace_dependency_catalogs
TO CURRENT_USER;

-- The migration owner reads every team's rows while FORCE RLS is lifted; the
-- policies call memoh_current_team_id(), which raises without a team context.
ALTER TABLE public.workspace_dependency_definitions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.workspace_dependency_catalogs NO FORCE ROW LEVEL SECURITY;

-- Copy only definitions whose release document hashes to its revision and
-- whose archive hashes to the digest that document declares. Verified copies
-- of one key are byte-identical, so any one of them stands for all.
-- CASE keeps the JSON parse behind the release hash check.
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

-- One catalog per source. The newest team catalog (by fetched_at, then
-- generation) supplies the current entries; every other id any team has seen
-- is kept as retired at the revision from the newest catalog that lists it.
-- References without a verified definition are dropped: loading them would
-- fail. A source left with no entries gets no row, so the next read refreshes
-- it from the registry instead of trusting an empty catalog.
WITH team_catalogs AS (
    SELECT team_id, source_url, generation, fetched_at,
           convert_from(catalog_bytes, 'UTF8')::jsonb AS doc
    FROM public.workspace_dependency_catalogs
),
base AS (
    SELECT DISTINCT ON (source_url) source_url, team_id, fetched_at
    FROM team_catalogs
    ORDER BY source_url, fetched_at DESC, generation DESC, team_id
),
verified_refs AS (
    SELECT c.source_url, c.team_id, c.generation, c.fetched_at,
           entry ->> 'id' AS dependency_id,
           entry ->> 'revision' AS revision,
           COALESCE((entry ->> 'retired')::boolean, false) AS retired
    FROM team_catalogs AS c
    CROSS JOIN LATERAL jsonb_array_elements(c.doc -> 'entries') AS entry
    JOIN template.workspace_dependency_definitions AS d
      ON d.source_url = c.source_url
     AND d.registry_id = 'memoh'
     AND d.dependency_id = entry ->> 'id'
     AND d.revision = entry ->> 'revision'
),
current_refs AS (
    SELECT r.source_url, r.dependency_id, r.revision
    FROM verified_refs AS r
    JOIN base AS b ON b.source_url = r.source_url AND b.team_id = r.team_id
    WHERE NOT r.retired
),
retired_refs AS (
    SELECT DISTINCT ON (r.source_url, r.dependency_id) r.source_url, r.dependency_id, r.revision
    FROM verified_refs AS r
    WHERE NOT EXISTS (
        SELECT 1 FROM current_refs AS c
        WHERE c.source_url = r.source_url AND c.dependency_id = r.dependency_id
    )
    ORDER BY r.source_url, r.dependency_id, r.fetched_at DESC, r.generation DESC, r.team_id
),
merged AS (
    SELECT source_url, dependency_id, json_build_object('id', dependency_id, 'revision', revision) AS ref
    FROM current_refs
    UNION ALL
    SELECT source_url, dependency_id, json_build_object('id', dependency_id, 'revision', revision, 'retired', true)
    FROM retired_refs
)
INSERT INTO template.workspace_dependency_catalogs (source_url, catalog_bytes, generation, fetched_at)
SELECT b.source_url,
       convert_to(json_build_object('entries', json_agg(m.ref ORDER BY m.dependency_id COLLATE "C"))::text, 'UTF8'),
       1,
       b.fetched_at
FROM base AS b
JOIN merged AS m ON m.source_url = b.source_url
GROUP BY b.source_url, b.fetched_at
ON CONFLICT (source_url) DO NOTHING;

ALTER TABLE public.workspace_dependency_definitions FORCE ROW LEVEL SECURITY;
ALTER TABLE public.workspace_dependency_catalogs FORCE ROW LEVEL SECURITY;
