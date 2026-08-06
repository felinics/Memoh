-- 0130_projects
-- Team-level Project collaboration spaces. A project contains one doc tree
-- (Wiki: nodes with type='doc', hierarchical via parent_id) and one flat
-- issue set (Issues: nodes with type='issue', parent_id always NULL,
-- projected as a kanban board). Docs and issues share one narrow node table;
-- issue-only structured fields live in a 1:1 detail table.
--
-- Two independent optimistic locks by design:
--   * project_nodes.version    — bumped only by title/body edits; every bump
--     lands an immutable snapshot in project_node_versions.
--   * project_issue_details.revision — bumped only by issue field changes
--     (status/assignee/priority/due); logged to project_issue_activity, not
--     the version table. Keeps "dragged a card" out of document history.
--
-- Authorship pairs (user/bot) use CHECK num_nonnulls(...) <= 1 rather than
-- exactly-one: member removal SET NULLs the user column, and an exactly-one
-- check would make that removal impossible. Both-NULL reads as "author no
-- longer a member". First version only ever writes the user column; the bot
-- columns exist so the future agent-tool phase is a zero-schema-change
-- increment.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ---------------------------------------------------------------------------
-- projects
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS public.projects (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id            UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                   REFERENCES public.teams(id) ON DELETE RESTRICT,
    name               TEXT        NOT NULL,
    description        TEXT        NOT NULL DEFAULT '',
    created_by_user_id UUID,
    deleted_at         TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT projects_team_key UNIQUE (team_id, id),
    CONSTRAINT projects_name_check CHECK (btrim(name) <> ''),
    CONSTRAINT projects_created_by_user_id_fkey
        FOREIGN KEY (team_id, created_by_user_id)
        REFERENCES public.team_members(team_id, user_id) ON DELETE SET NULL (created_by_user_id)
);

CREATE INDEX IF NOT EXISTS idx_projects_team_live
    ON public.projects (team_id, created_at ASC)
    WHERE deleted_at IS NULL;

ALTER TABLE public.projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.projects FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS projects_team_select ON public.projects;
DROP POLICY IF EXISTS projects_team_insert ON public.projects;
DROP POLICY IF EXISTS projects_team_update ON public.projects;
DROP POLICY IF EXISTS projects_team_delete ON public.projects;

CREATE POLICY projects_team_select ON public.projects
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY projects_team_insert ON public.projects
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY projects_team_update ON public.projects
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY projects_team_delete ON public.projects
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- ---------------------------------------------------------------------------
-- project_nodes — the single carrier for tree structure and content
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS public.project_nodes (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id            UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                   REFERENCES public.teams(id) ON DELETE RESTRICT,
    project_id         UUID        NOT NULL,
    type               TEXT        NOT NULL,
    parent_id          UUID,
    -- Lexorank-style ordering key. For docs: position among siblings. For
    -- issues: position within the kanban column of the current status.
    rank               TEXT        NOT NULL,
    title              TEXT        NOT NULL,
    body               TEXT        NOT NULL DEFAULT '',
    -- Optimistic lock for title/body. Bumped only by content edits; each
    -- bump writes an immutable snapshot row (see project_node_versions).
    version            INT         NOT NULL DEFAULT 1,
    created_by_user_id UUID,
    created_by_bot_id  UUID,
    updated_by_user_id UUID,
    updated_by_bot_id  UUID,
    deleted_at         TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT project_nodes_team_key UNIQUE (team_id, id),
    CONSTRAINT project_nodes_type_check CHECK (type IN ('doc', 'issue')),
    -- Issues are flat by design: "should issues get subtasks later" must be
    -- a constraint change, not a schema change.
    CONSTRAINT project_nodes_issue_flat_check CHECK (type <> 'issue' OR parent_id IS NULL),
    CONSTRAINT project_nodes_rank_check CHECK (rank <> ''),
    CONSTRAINT project_nodes_version_check CHECK (version >= 1),
    CONSTRAINT project_nodes_created_by_check CHECK (num_nonnulls(created_by_user_id, created_by_bot_id) <= 1),
    CONSTRAINT project_nodes_updated_by_check CHECK (num_nonnulls(updated_by_user_id, updated_by_bot_id) <= 1),
    CONSTRAINT project_nodes_project_id_fkey
        FOREIGN KEY (team_id, project_id)
        REFERENCES public.projects(team_id, id) ON DELETE CASCADE,
    CONSTRAINT project_nodes_parent_id_fkey
        FOREIGN KEY (team_id, parent_id)
        REFERENCES public.project_nodes(team_id, id) ON DELETE CASCADE,
    CONSTRAINT project_nodes_created_by_user_id_fkey
        FOREIGN KEY (team_id, created_by_user_id)
        REFERENCES public.team_members(team_id, user_id) ON DELETE SET NULL (created_by_user_id),
    CONSTRAINT project_nodes_created_by_bot_id_fkey
        FOREIGN KEY (team_id, created_by_bot_id)
        REFERENCES public.bots(team_id, id) ON DELETE SET NULL (created_by_bot_id),
    CONSTRAINT project_nodes_updated_by_user_id_fkey
        FOREIGN KEY (team_id, updated_by_user_id)
        REFERENCES public.team_members(team_id, user_id) ON DELETE SET NULL (updated_by_user_id),
    CONSTRAINT project_nodes_updated_by_bot_id_fkey
        FOREIGN KEY (team_id, updated_by_bot_id)
        REFERENCES public.bots(team_id, id) ON DELETE SET NULL (updated_by_bot_id)
);

-- Doc tree: one ordered scan per sibling group.
CREATE INDEX IF NOT EXISTS idx_project_nodes_tree
    ON public.project_nodes (team_id, project_id, parent_id, rank)
    WHERE deleted_at IS NULL;

-- Kanban: all live issues of a project in one range scan.
CREATE INDEX IF NOT EXISTS idx_project_nodes_board
    ON public.project_nodes (team_id, project_id, type)
    WHERE deleted_at IS NULL;

-- Substring search. pg_trgm instead of tsvector on purpose: the built-in
-- text search parsers do not segment CJK text, ILIKE + trigram GIN does.
CREATE INDEX IF NOT EXISTS idx_project_nodes_title_trgm
    ON public.project_nodes USING gin (title gin_trgm_ops)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_project_nodes_body_trgm
    ON public.project_nodes USING gin (body gin_trgm_ops)
    WHERE deleted_at IS NULL;

ALTER TABLE public.project_nodes ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.project_nodes FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS project_nodes_team_select ON public.project_nodes;
DROP POLICY IF EXISTS project_nodes_team_insert ON public.project_nodes;
DROP POLICY IF EXISTS project_nodes_team_update ON public.project_nodes;
DROP POLICY IF EXISTS project_nodes_team_delete ON public.project_nodes;

CREATE POLICY project_nodes_team_select ON public.project_nodes
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY project_nodes_team_insert ON public.project_nodes
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY project_nodes_team_update ON public.project_nodes
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY project_nodes_team_delete ON public.project_nodes
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- ---------------------------------------------------------------------------
-- project_node_versions — immutable title/body snapshots
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS public.project_node_versions (
    team_id        UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                               REFERENCES public.teams(id) ON DELETE RESTRICT,
    node_id        UUID        NOT NULL,
    version        INT         NOT NULL,
    title          TEXT        NOT NULL,
    body           TEXT        NOT NULL,
    editor_user_id UUID,
    editor_bot_id  UUID,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- The debounce/merge window updates the newest row in place until the
    -- window closes; rows below the node's max version are immutable.
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, node_id, version),
    CONSTRAINT project_node_versions_version_check CHECK (version >= 1),
    CONSTRAINT project_node_versions_editor_check CHECK (num_nonnulls(editor_user_id, editor_bot_id) <= 1),
    CONSTRAINT project_node_versions_node_id_fkey
        FOREIGN KEY (team_id, node_id)
        REFERENCES public.project_nodes(team_id, id) ON DELETE CASCADE,
    CONSTRAINT project_node_versions_editor_user_id_fkey
        FOREIGN KEY (team_id, editor_user_id)
        REFERENCES public.team_members(team_id, user_id) ON DELETE SET NULL (editor_user_id),
    CONSTRAINT project_node_versions_editor_bot_id_fkey
        FOREIGN KEY (team_id, editor_bot_id)
        REFERENCES public.bots(team_id, id) ON DELETE SET NULL (editor_bot_id)
);

ALTER TABLE public.project_node_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.project_node_versions FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS project_node_versions_team_select ON public.project_node_versions;
DROP POLICY IF EXISTS project_node_versions_team_insert ON public.project_node_versions;
DROP POLICY IF EXISTS project_node_versions_team_update ON public.project_node_versions;
DROP POLICY IF EXISTS project_node_versions_team_delete ON public.project_node_versions;

CREATE POLICY project_node_versions_team_select ON public.project_node_versions
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY project_node_versions_team_insert ON public.project_node_versions
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY project_node_versions_team_update ON public.project_node_versions
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY project_node_versions_team_delete ON public.project_node_versions
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- ---------------------------------------------------------------------------
-- project_issue_details — 1:1 issue extension
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS public.project_issue_details (
    team_id          UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                 REFERENCES public.teams(id) ON DELETE RESTRICT,
    node_id          UUID        NOT NULL,
    status           TEXT        NOT NULL DEFAULT 'todo',
    assignee_user_id UUID,
    assignee_bot_id  UUID,
    priority         TEXT,
    due_at           TIMESTAMPTZ,
    -- Optimistic lock for issue fields, independent from the content
    -- version: editing a description and dragging the card must not
    -- conflict with each other.
    revision         INT         NOT NULL DEFAULT 1,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, node_id),
    CONSTRAINT project_issue_details_status_check
        CHECK (status IN ('todo', 'in_progress', 'done', 'cancelled')),
    CONSTRAINT project_issue_details_priority_check
        CHECK (priority IS NULL OR priority IN ('low', 'medium', 'high', 'urgent')),
    CONSTRAINT project_issue_details_assignee_check CHECK (num_nonnulls(assignee_user_id, assignee_bot_id) <= 1),
    CONSTRAINT project_issue_details_revision_check CHECK (revision >= 1),
    CONSTRAINT project_issue_details_node_id_fkey
        FOREIGN KEY (team_id, node_id)
        REFERENCES public.project_nodes(team_id, id) ON DELETE CASCADE,
    CONSTRAINT project_issue_details_assignee_user_id_fkey
        FOREIGN KEY (team_id, assignee_user_id)
        REFERENCES public.team_members(team_id, user_id) ON DELETE SET NULL (assignee_user_id),
    CONSTRAINT project_issue_details_assignee_bot_id_fkey
        FOREIGN KEY (team_id, assignee_bot_id)
        REFERENCES public.bots(team_id, id) ON DELETE SET NULL (assignee_bot_id)
);

ALTER TABLE public.project_issue_details ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.project_issue_details FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS project_issue_details_team_select ON public.project_issue_details;
DROP POLICY IF EXISTS project_issue_details_team_insert ON public.project_issue_details;
DROP POLICY IF EXISTS project_issue_details_team_update ON public.project_issue_details;
DROP POLICY IF EXISTS project_issue_details_team_delete ON public.project_issue_details;

CREATE POLICY project_issue_details_team_select ON public.project_issue_details
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY project_issue_details_team_insert ON public.project_issue_details
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY project_issue_details_team_update ON public.project_issue_details
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY project_issue_details_team_delete ON public.project_issue_details
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- ---------------------------------------------------------------------------
-- project_issue_activity — audit trail for issue field changes
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS public.project_issue_activity (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id       UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                              REFERENCES public.teams(id) ON DELETE RESTRICT,
    node_id       UUID        NOT NULL,
    actor_user_id UUID,
    actor_bot_id  UUID,
    field         TEXT        NOT NULL,
    old_value     TEXT,
    new_value     TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT project_issue_activity_team_key UNIQUE (team_id, id),
    CONSTRAINT project_issue_activity_field_check
        CHECK (field IN ('status', 'assignee', 'priority', 'due_at')),
    CONSTRAINT project_issue_activity_actor_check CHECK (num_nonnulls(actor_user_id, actor_bot_id) <= 1),
    CONSTRAINT project_issue_activity_node_id_fkey
        FOREIGN KEY (team_id, node_id)
        REFERENCES public.project_nodes(team_id, id) ON DELETE CASCADE,
    CONSTRAINT project_issue_activity_actor_user_id_fkey
        FOREIGN KEY (team_id, actor_user_id)
        REFERENCES public.team_members(team_id, user_id) ON DELETE SET NULL (actor_user_id),
    CONSTRAINT project_issue_activity_actor_bot_id_fkey
        FOREIGN KEY (team_id, actor_bot_id)
        REFERENCES public.bots(team_id, id) ON DELETE SET NULL (actor_bot_id)
);

CREATE INDEX IF NOT EXISTS idx_project_issue_activity_node
    ON public.project_issue_activity (team_id, node_id, created_at ASC);

ALTER TABLE public.project_issue_activity ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.project_issue_activity FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS project_issue_activity_team_select ON public.project_issue_activity;
DROP POLICY IF EXISTS project_issue_activity_team_insert ON public.project_issue_activity;
DROP POLICY IF EXISTS project_issue_activity_team_update ON public.project_issue_activity;
DROP POLICY IF EXISTS project_issue_activity_team_delete ON public.project_issue_activity;

CREATE POLICY project_issue_activity_team_select ON public.project_issue_activity
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY project_issue_activity_team_insert ON public.project_issue_activity
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY project_issue_activity_team_update ON public.project_issue_activity
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY project_issue_activity_team_delete ON public.project_issue_activity
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- ---------------------------------------------------------------------------
-- project_comments — shared by docs and issues
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS public.project_comments (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id        UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                               REFERENCES public.teams(id) ON DELETE RESTRICT,
    node_id        UUID        NOT NULL,
    author_user_id UUID,
    author_bot_id  UUID,
    body           TEXT        NOT NULL,
    deleted_at     TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT project_comments_team_key UNIQUE (team_id, id),
    CONSTRAINT project_comments_body_check CHECK (btrim(body) <> ''),
    CONSTRAINT project_comments_author_check CHECK (num_nonnulls(author_user_id, author_bot_id) <= 1),
    CONSTRAINT project_comments_node_id_fkey
        FOREIGN KEY (team_id, node_id)
        REFERENCES public.project_nodes(team_id, id) ON DELETE CASCADE,
    CONSTRAINT project_comments_author_user_id_fkey
        FOREIGN KEY (team_id, author_user_id)
        REFERENCES public.team_members(team_id, user_id) ON DELETE SET NULL (author_user_id),
    CONSTRAINT project_comments_author_bot_id_fkey
        FOREIGN KEY (team_id, author_bot_id)
        REFERENCES public.bots(team_id, id) ON DELETE SET NULL (author_bot_id)
);

CREATE INDEX IF NOT EXISTS idx_project_comments_node
    ON public.project_comments (team_id, node_id, created_at ASC)
    WHERE deleted_at IS NULL;

ALTER TABLE public.project_comments ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.project_comments FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS project_comments_team_select ON public.project_comments;
DROP POLICY IF EXISTS project_comments_team_insert ON public.project_comments;
DROP POLICY IF EXISTS project_comments_team_update ON public.project_comments;
DROP POLICY IF EXISTS project_comments_team_delete ON public.project_comments;

CREATE POLICY project_comments_team_select ON public.project_comments
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY project_comments_team_insert ON public.project_comments
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY project_comments_team_update ON public.project_comments
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY project_comments_team_delete ON public.project_comments
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- ---------------------------------------------------------------------------
-- project_node_links — node → node references (first version: issue → doc)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS public.project_node_links (
    team_id        UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                               REFERENCES public.teams(id) ON DELETE RESTRICT,
    source_node_id UUID        NOT NULL,
    target_node_id UUID        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, source_node_id, target_node_id),
    CONSTRAINT project_node_links_no_self_check CHECK (source_node_id <> target_node_id),
    CONSTRAINT project_node_links_source_fkey
        FOREIGN KEY (team_id, source_node_id)
        REFERENCES public.project_nodes(team_id, id) ON DELETE CASCADE,
    CONSTRAINT project_node_links_target_fkey
        FOREIGN KEY (team_id, target_node_id)
        REFERENCES public.project_nodes(team_id, id) ON DELETE CASCADE
);

-- Backlinks ("which issues reference this doc").
CREATE INDEX IF NOT EXISTS idx_project_node_links_target
    ON public.project_node_links (team_id, target_node_id);

ALTER TABLE public.project_node_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.project_node_links FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS project_node_links_team_select ON public.project_node_links;
DROP POLICY IF EXISTS project_node_links_team_insert ON public.project_node_links;
DROP POLICY IF EXISTS project_node_links_team_update ON public.project_node_links;
DROP POLICY IF EXISTS project_node_links_team_delete ON public.project_node_links;

CREATE POLICY project_node_links_team_select ON public.project_node_links
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY project_node_links_team_insert ON public.project_node_links
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY project_node_links_team_update ON public.project_node_links
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY project_node_links_team_delete ON public.project_node_links
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- ---------------------------------------------------------------------------
-- project_labels — per-project label definitions
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS public.project_labels (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id    UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                           REFERENCES public.teams(id) ON DELETE RESTRICT,
    project_id UUID        NOT NULL,
    name       TEXT        NOT NULL,
    color      TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT project_labels_team_key UNIQUE (team_id, id),
    CONSTRAINT project_labels_name_unique UNIQUE (team_id, project_id, name),
    CONSTRAINT project_labels_name_check CHECK (btrim(name) <> ''),
    CONSTRAINT project_labels_project_id_fkey
        FOREIGN KEY (team_id, project_id)
        REFERENCES public.projects(team_id, id) ON DELETE CASCADE
);

ALTER TABLE public.project_labels ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.project_labels FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS project_labels_team_select ON public.project_labels;
DROP POLICY IF EXISTS project_labels_team_insert ON public.project_labels;
DROP POLICY IF EXISTS project_labels_team_update ON public.project_labels;
DROP POLICY IF EXISTS project_labels_team_delete ON public.project_labels;

CREATE POLICY project_labels_team_select ON public.project_labels
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY project_labels_team_insert ON public.project_labels
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY project_labels_team_update ON public.project_labels
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY project_labels_team_delete ON public.project_labels
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- ---------------------------------------------------------------------------
-- project_node_labels — node ↔ label assignments
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS public.project_node_labels (
    team_id    UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                           REFERENCES public.teams(id) ON DELETE RESTRICT,
    node_id    UUID        NOT NULL,
    label_id   UUID        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, node_id, label_id),
    CONSTRAINT project_node_labels_node_id_fkey
        FOREIGN KEY (team_id, node_id)
        REFERENCES public.project_nodes(team_id, id) ON DELETE CASCADE,
    CONSTRAINT project_node_labels_label_id_fkey
        FOREIGN KEY (team_id, label_id)
        REFERENCES public.project_labels(team_id, id) ON DELETE CASCADE
);

-- "Which nodes carry this label" (label delete preview, filtering).
CREATE INDEX IF NOT EXISTS idx_project_node_labels_label
    ON public.project_node_labels (team_id, label_id);

ALTER TABLE public.project_node_labels ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.project_node_labels FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS project_node_labels_team_select ON public.project_node_labels;
DROP POLICY IF EXISTS project_node_labels_team_insert ON public.project_node_labels;
DROP POLICY IF EXISTS project_node_labels_team_update ON public.project_node_labels;
DROP POLICY IF EXISTS project_node_labels_team_delete ON public.project_node_labels;

CREATE POLICY project_node_labels_team_select ON public.project_node_labels
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY project_node_labels_team_insert ON public.project_node_labels
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY project_node_labels_team_update ON public.project_node_labels
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY project_node_labels_team_delete ON public.project_node_labels
    FOR DELETE USING (team_id = public.memoh_current_team_id());
