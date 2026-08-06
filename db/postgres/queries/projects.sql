-- Project collaboration spaces: Wiki doc tree + Issues kanban over one node
-- table. Content writes are guarded by project_nodes.version, issue field
-- writes by project_issue_details.revision — two independent optimistic
-- locks so editing a description never conflicts with dragging the card.

-- name: CreateProject :one
INSERT INTO projects (name, description, created_by_user_id)
VALUES (sqlc.arg(name), sqlc.arg(description), sqlc.narg(created_by_user_id)::uuid)
RETURNING *;

-- name: ListProjects :many
-- Carries the issue tallies the project cards render, so a list of N projects
-- stays one query instead of N board fetches. Buckets follow the
-- issue-tracker convention: cancelled counts as closed, not as open work.
SELECT p.*,
  COALESCE(c.open_count, 0)::bigint AS open_issue_count,
  COALESCE(c.closed_count, 0)::bigint AS closed_issue_count
FROM projects p
LEFT JOIN (
  SELECT n.project_id,
    COUNT(*) FILTER (WHERE d.status IN ('todo', 'in_progress')) AS open_count,
    COUNT(*) FILTER (WHERE d.status IN ('done', 'cancelled')) AS closed_count
  FROM project_nodes n
  JOIN project_issue_details d ON d.team_id = n.team_id AND d.node_id = n.id
  WHERE n.team_id = public.memoh_current_team_id()
    AND n.type = 'issue' AND n.deleted_at IS NULL
  GROUP BY n.project_id
) c ON c.project_id = p.id
WHERE p.team_id = public.memoh_current_team_id() AND p.deleted_at IS NULL
ORDER BY p.created_at ASC, p.id ASC;

-- name: GetProject :one
SELECT * FROM projects
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(project_id) AND deleted_at IS NULL;

-- name: UpdateProject :one
UPDATE projects SET
  name = sqlc.arg(name),
  description = sqlc.arg(description),
  updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(project_id) AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteProject :execrows
UPDATE projects SET deleted_at = now(), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(project_id) AND deleted_at IS NULL;

-- name: CreateProjectNode :one
INSERT INTO project_nodes (
  project_id, type, parent_id, rank, title, body,
  created_by_user_id, created_by_bot_id, updated_by_user_id, updated_by_bot_id
)
VALUES (
  sqlc.arg(project_id), sqlc.arg(type), sqlc.narg(parent_id)::uuid,
  sqlc.arg(rank), sqlc.arg(title), sqlc.arg(body),
  sqlc.narg(created_by_user_id)::uuid, sqlc.narg(created_by_bot_id)::uuid,
  sqlc.narg(updated_by_user_id)::uuid, sqlc.narg(updated_by_bot_id)::uuid
)
RETURNING *;

-- name: GetProjectNode :one
SELECT * FROM project_nodes
WHERE team_id = public.memoh_current_team_id()
  AND project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(node_id) AND deleted_at IS NULL;

-- name: ListProjectDocNodes :many
SELECT id, parent_id, rank, title, version, created_at, updated_at
FROM project_nodes
WHERE team_id = public.memoh_current_team_id()
  AND project_id = sqlc.arg(project_id)
  AND type = 'doc' AND deleted_at IS NULL
ORDER BY rank ASC, id ASC;

-- name: UpdateProjectNodeContent :one
-- Zero rows means either "gone" or "version conflict"; the service re-reads
-- to tell a 404 from a 409.
UPDATE project_nodes SET
  title = sqlc.arg(title),
  body = sqlc.arg(body),
  version = version + 1,
  updated_by_user_id = sqlc.narg(updated_by_user_id)::uuid,
  updated_by_bot_id = sqlc.narg(updated_by_bot_id)::uuid,
  updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(node_id)
  AND version = sqlc.arg(expected_version)
  AND deleted_at IS NULL
RETURNING *;

-- name: MoveProjectNode :one
-- Doc tree only. Issue rank changes ride along with the issue-field update
-- so drag-to-column stays one atomic write.
UPDATE project_nodes SET
  parent_id = sqlc.narg(parent_id)::uuid,
  rank = sqlc.arg(rank),
  updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(node_id)
  AND type = 'doc' AND deleted_at IS NULL
RETURNING *;

-- name: UpdateProjectNodeRank :execrows
UPDATE project_nodes SET rank = sqlc.arg(rank), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(node_id) AND deleted_at IS NULL;

-- name: AcquireProjectTreeLock :exec
-- Transaction-scoped advisory lock serializing tree mutations (move/delete)
-- per project. Without it two concurrent moves can each pass the cycle walk
-- and commit a parent loop.
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(project_id)::text, 42));

-- name: GetProjectNodeByID :one
-- Project-agnostic lookup: link targets may live in another project
-- (cross-project references are allowed and render permission-gated).
SELECT * FROM project_nodes
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(node_id) AND deleted_at IS NULL;

-- name: GetProjectNodeParent :one
-- One step of the ancestor walk the service runs for move cycle detection
-- (sqlc's analyzer cannot reference a recursive CTE from a SELECT, so the
-- walk lives in Go). Returns rows regardless of deleted_at: soft-deleted
-- nodes still hang off the tree and must not become ancestors of live ones.
SELECT id, parent_id FROM project_nodes
WHERE team_id = public.memoh_current_team_id() AND id = sqlc.arg(node_id);

-- name: SoftDeleteProjectNodeSubtree :execrows
WITH RECURSIVE subtree AS (
  SELECT r.id FROM project_nodes r
  WHERE r.team_id = public.memoh_current_team_id()
    AND r.project_id = sqlc.arg(project_id)
    AND r.id = sqlc.arg(node_id)
  UNION ALL
  SELECT n.id FROM project_nodes n
  JOIN subtree s ON n.parent_id = s.id
  WHERE n.team_id = public.memoh_current_team_id()
)
UPDATE project_nodes SET deleted_at = now(), updated_at = now()
WHERE project_nodes.team_id = public.memoh_current_team_id()
  AND project_nodes.id IN (SELECT t.id FROM subtree t)
  AND project_nodes.deleted_at IS NULL;

-- name: MaxProjectDocSiblingRank :one
SELECT COALESCE(MAX(rank), '')::text AS max_rank
FROM project_nodes
WHERE team_id = public.memoh_current_team_id()
  AND project_id = sqlc.arg(project_id)
  AND type = 'doc'
  AND parent_id IS NOT DISTINCT FROM sqlc.narg(parent_id)::uuid
  AND deleted_at IS NULL;

-- name: ListProjectDocSiblingRanks :many
SELECT id, rank FROM project_nodes
WHERE team_id = public.memoh_current_team_id()
  AND project_id = sqlc.arg(project_id)
  AND type = 'doc'
  AND parent_id IS NOT DISTINCT FROM sqlc.narg(parent_id)::uuid
  AND deleted_at IS NULL
ORDER BY rank ASC, id ASC;

-- name: MaxProjectIssueRank :one
SELECT COALESCE(MAX(n.rank), '')::text AS max_rank
FROM project_nodes n
JOIN project_issue_details d ON d.team_id = n.team_id AND d.node_id = n.id
WHERE n.team_id = public.memoh_current_team_id()
  AND n.project_id = sqlc.arg(project_id)
  AND n.type = 'issue'
  AND d.status = sqlc.arg(status)
  AND n.deleted_at IS NULL;

-- name: ListProjectIssueRanksByStatus :many
SELECT n.id, n.rank
FROM project_nodes n
JOIN project_issue_details d ON d.team_id = n.team_id AND d.node_id = n.id
WHERE n.team_id = public.memoh_current_team_id()
  AND n.project_id = sqlc.arg(project_id)
  AND n.type = 'issue'
  AND d.status = sqlc.arg(status)
  AND n.deleted_at IS NULL
ORDER BY n.rank ASC, n.id ASC;

-- name: ListProjectIssues :many
SELECT
  n.id, n.project_id, n.rank, n.title, n.version, n.created_at, n.updated_at,
  d.status, d.assignee_user_id, d.assignee_bot_id, d.priority, d.due_at, d.revision
FROM project_nodes n
JOIN project_issue_details d ON d.team_id = n.team_id AND d.node_id = n.id
WHERE n.team_id = public.memoh_current_team_id()
  AND n.project_id = sqlc.arg(project_id)
  AND n.type = 'issue' AND n.deleted_at IS NULL
ORDER BY d.status ASC, n.rank ASC, n.id ASC;

-- name: CreateProjectIssueDetails :one
INSERT INTO project_issue_details (
  node_id, status, assignee_user_id, assignee_bot_id, priority, due_at
)
VALUES (
  sqlc.arg(node_id), sqlc.arg(status),
  sqlc.narg(assignee_user_id)::uuid, sqlc.narg(assignee_bot_id)::uuid,
  sqlc.narg(priority)::text, sqlc.narg(due_at)::timestamptz
)
RETURNING *;

-- name: GetProjectIssueDetails :one
SELECT * FROM project_issue_details
WHERE team_id = public.memoh_current_team_id() AND node_id = sqlc.arg(node_id);

-- name: UpdateProjectIssueDetails :one
-- The service resolves the final field values in Go (partial-update
-- semantics live there); the revision guard turns any concurrent change
-- into zero rows → 409.
UPDATE project_issue_details SET
  status = sqlc.arg(status),
  assignee_user_id = sqlc.narg(assignee_user_id)::uuid,
  assignee_bot_id = sqlc.narg(assignee_bot_id)::uuid,
  priority = sqlc.narg(priority)::text,
  due_at = sqlc.narg(due_at)::timestamptz,
  revision = revision + 1,
  updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND node_id = sqlc.arg(node_id)
  AND revision = sqlc.arg(expected_revision)
RETURNING *;

-- name: InsertProjectIssueActivity :exec
INSERT INTO project_issue_activity (
  node_id, actor_user_id, actor_bot_id, field, old_value, new_value
)
VALUES (
  sqlc.arg(node_id),
  sqlc.narg(actor_user_id)::uuid, sqlc.narg(actor_bot_id)::uuid,
  sqlc.arg(field), sqlc.narg(old_value)::text, sqlc.narg(new_value)::text
);

-- name: ListProjectIssueActivity :many
SELECT * FROM project_issue_activity
WHERE team_id = public.memoh_current_team_id() AND node_id = sqlc.arg(node_id)
ORDER BY created_at ASC, id ASC;

-- name: InsertProjectNodeVersion :exec
INSERT INTO project_node_versions (
  node_id, version, title, body, editor_user_id, editor_bot_id
)
VALUES (
  sqlc.arg(node_id), sqlc.arg(version), sqlc.arg(title), sqlc.arg(body),
  sqlc.narg(editor_user_id)::uuid, sqlc.narg(editor_bot_id)::uuid
);

-- name: GetLatestProjectNodeVersion :one
SELECT * FROM project_node_versions
WHERE team_id = public.memoh_current_team_id() AND node_id = sqlc.arg(node_id)
ORDER BY version DESC
LIMIT 1;

-- name: RenumberProjectNodeVersion :execrows
-- Merge-window path: consecutive edits by the same author update the newest
-- snapshot in place, carrying it to the node's new version number. Closed
-- versions (anything below the max) are never touched.
UPDATE project_node_versions SET
  version = sqlc.arg(new_version),
  title = sqlc.arg(title),
  body = sqlc.arg(body),
  updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND node_id = sqlc.arg(node_id)
  AND version = sqlc.arg(old_version);

-- name: ListProjectNodeVersions :many
SELECT node_id, version, title, editor_user_id, editor_bot_id, created_at, updated_at
FROM project_node_versions
WHERE team_id = public.memoh_current_team_id() AND node_id = sqlc.arg(node_id)
ORDER BY version DESC;

-- name: GetProjectNodeVersion :one
SELECT * FROM project_node_versions
WHERE team_id = public.memoh_current_team_id()
  AND node_id = sqlc.arg(node_id) AND version = sqlc.arg(version);

-- name: CreateProjectComment :one
INSERT INTO project_comments (node_id, author_user_id, author_bot_id, body)
VALUES (
  sqlc.arg(node_id),
  sqlc.narg(author_user_id)::uuid, sqlc.narg(author_bot_id)::uuid,
  sqlc.arg(body)
)
RETURNING *;

-- name: ListProjectComments :many
SELECT * FROM project_comments
WHERE team_id = public.memoh_current_team_id()
  AND node_id = sqlc.arg(node_id) AND deleted_at IS NULL
ORDER BY created_at ASC, id ASC;

-- name: GetProjectComment :one
SELECT * FROM project_comments
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(comment_id) AND deleted_at IS NULL;

-- name: UpdateProjectComment :one
UPDATE project_comments SET body = sqlc.arg(body), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(comment_id) AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteProjectComment :execrows
UPDATE project_comments SET deleted_at = now(), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(comment_id) AND deleted_at IS NULL;

-- name: CreateProjectNodeLink :exec
INSERT INTO project_node_links (source_node_id, target_node_id)
VALUES (sqlc.arg(source_node_id), sqlc.arg(target_node_id))
ON CONFLICT (team_id, source_node_id, target_node_id) DO NOTHING;

-- name: DeleteProjectNodeLink :execrows
DELETE FROM project_node_links
WHERE team_id = public.memoh_current_team_id()
  AND source_node_id = sqlc.arg(source_node_id)
  AND target_node_id = sqlc.arg(target_node_id);

-- name: ListProjectNodeLinks :many
SELECT n.id, n.project_id, n.type, n.title
FROM project_node_links k
JOIN project_nodes n ON n.team_id = k.team_id AND n.id = k.target_node_id
WHERE k.team_id = public.memoh_current_team_id()
  AND k.source_node_id = sqlc.arg(node_id)
  AND n.deleted_at IS NULL
ORDER BY n.title ASC, n.id ASC;

-- name: ListProjectNodeBacklinks :many
SELECT n.id, n.project_id, n.type, n.title
FROM project_node_links k
JOIN project_nodes n ON n.team_id = k.team_id AND n.id = k.source_node_id
WHERE k.team_id = public.memoh_current_team_id()
  AND k.target_node_id = sqlc.arg(node_id)
  AND n.deleted_at IS NULL
ORDER BY n.title ASC, n.id ASC;

-- name: CreateProjectLabel :one
INSERT INTO project_labels (project_id, name, color)
VALUES (sqlc.arg(project_id), sqlc.arg(name), sqlc.arg(color))
RETURNING *;

-- name: ListProjectLabels :many
SELECT * FROM project_labels
WHERE team_id = public.memoh_current_team_id()
  AND project_id = sqlc.arg(project_id)
ORDER BY name ASC, id ASC;

-- name: UpdateProjectLabel :one
UPDATE project_labels SET name = sqlc.arg(name), color = sqlc.arg(color)
WHERE team_id = public.memoh_current_team_id()
  AND project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(label_id)
RETURNING *;

-- name: DeleteProjectLabel :execrows
DELETE FROM project_labels
WHERE team_id = public.memoh_current_team_id()
  AND project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(label_id);

-- name: DeleteProjectNodeLabels :exec
DELETE FROM project_node_labels
WHERE team_id = public.memoh_current_team_id()
  AND node_id = sqlc.arg(node_id);

-- name: InsertProjectNodeLabel :exec
INSERT INTO project_node_labels (node_id, label_id)
VALUES (sqlc.arg(node_id), sqlc.arg(label_id))
ON CONFLICT (team_id, node_id, label_id) DO NOTHING;

-- name: ListProjectNodeLabelsForNode :many
SELECT l.* FROM project_node_labels nl
JOIN project_labels l ON l.team_id = nl.team_id AND l.id = nl.label_id
WHERE nl.team_id = public.memoh_current_team_id()
  AND nl.node_id = sqlc.arg(node_id)
ORDER BY l.name ASC, l.id ASC;

-- name: ListProjectNodeLabelsByProject :many
SELECT nl.node_id, l.id AS label_id, l.name, l.color
FROM project_node_labels nl
JOIN project_labels l ON l.team_id = nl.team_id AND l.id = nl.label_id
JOIN project_nodes n ON n.team_id = nl.team_id AND n.id = nl.node_id
WHERE nl.team_id = public.memoh_current_team_id()
  AND n.project_id = sqlc.arg(project_id)
  AND n.deleted_at IS NULL
ORDER BY nl.node_id ASC, l.name ASC;

-- name: SearchProjectNodes :many
-- Substring search over title+body. The service escapes ILIKE wildcards in
-- the query string; the trigram GIN indexes serve the leading-wildcard scan.
SELECT
  n.id, n.project_id, p.name AS project_name, n.type, n.title,
  left(n.body, 300)::text AS snippet, n.updated_at
FROM project_nodes n
JOIN projects p ON p.team_id = n.team_id AND p.id = n.project_id
WHERE n.team_id = public.memoh_current_team_id()
  AND n.deleted_at IS NULL AND p.deleted_at IS NULL
  AND (n.title ILIKE sqlc.arg(pattern) OR n.body ILIKE sqlc.arg(pattern))
  AND (sqlc.narg(project_id)::uuid IS NULL OR n.project_id = sqlc.narg(project_id)::uuid)
  AND (sqlc.narg(node_type)::text IS NULL OR n.type = sqlc.narg(node_type)::text)
ORDER BY n.updated_at DESC, n.id ASC
LIMIT sqlc.arg(limit_count);
