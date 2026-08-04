-- 0129_bot_projects
-- Named per-bot project directories. A project pins a workspace target
-- (the native container workspace, or a remote runtime binding) together
-- with an absolute directory path. bot_sessions.project_id binds a session
-- to a project immutably at creation time; the session's working directory
-- is derived from the project for its whole life.
--
-- Delete semantics are two-tiered on purpose:
--   * API delete = archive (archived_at). Existing sessions keep resolving
--     their project so their working directory never changes underneath
--     them; only new sessions are refused.
--   * Unbinding a remote runtime hard-deletes its projects via the CASCADE
--     below, and bot_sessions.project_id degrades to NULL (no project).

CREATE TABLE IF NOT EXISTS public.bot_projects (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id            UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                   REFERENCES public.teams(id) ON DELETE RESTRICT,
    bot_id             UUID        NOT NULL,
    name               TEXT        NOT NULL,
    target_kind        TEXT        NOT NULL,
    -- Non-null exactly when target_kind = 'remote'. Unbinding the remote
    -- runtime cascades here, which is what removes its projects.
    remote_binding_id  UUID,
    path               TEXT        NOT NULL,
    created_by_user_id UUID,
    archived_at        TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT bot_projects_team_key UNIQUE (team_id, id),
    -- Creator reference follows the platform convention: composite key into
    -- team_members with a column-list SET NULL, so clearing the reference can
    -- never clear the NOT NULL team_id.
    CONSTRAINT bot_projects_created_by_user_id_fkey
        FOREIGN KEY (team_id, created_by_user_id)
        REFERENCES public.team_members(team_id, user_id) ON DELETE SET NULL (created_by_user_id),
    CONSTRAINT bot_projects_target_kind_check CHECK (target_kind IN ('native', 'remote')),
    CONSTRAINT bot_projects_binding_check
        CHECK ((target_kind = 'remote') = (remote_binding_id IS NOT NULL)),
    CONSTRAINT bot_projects_bot_id_fkey
        FOREIGN KEY (team_id, bot_id)
        REFERENCES public.bots(team_id, id) ON DELETE CASCADE,
    CONSTRAINT bot_projects_remote_binding_fkey
        FOREIGN KEY (team_id, remote_binding_id)
        REFERENCES public.bot_remote_runtime_bindings(team_id, id) ON DELETE CASCADE
);

-- One live project per directory per target. Native rows carry a NULL
-- remote_binding_id and NULLs never collide in a unique index, so a zero
-- UUID sentinel folds them into the same uniqueness scope.
CREATE UNIQUE INDEX IF NOT EXISTS bot_projects_target_path_unique
    ON public.bot_projects (
        team_id, bot_id,
        COALESCE(remote_binding_id, '00000000-0000-0000-0000-000000000000'::uuid),
        path
    ) WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_bot_projects_bot
    ON public.bot_projects (team_id, bot_id, created_at DESC)
    WHERE archived_at IS NULL;

ALTER TABLE public.bot_projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_projects FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS bot_projects_team_select ON public.bot_projects;
DROP POLICY IF EXISTS bot_projects_team_insert ON public.bot_projects;
DROP POLICY IF EXISTS bot_projects_team_update ON public.bot_projects;
DROP POLICY IF EXISTS bot_projects_team_delete ON public.bot_projects;

CREATE POLICY bot_projects_team_select ON public.bot_projects
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_projects_team_insert ON public.bot_projects
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_projects_team_update ON public.bot_projects
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_projects_team_delete ON public.bot_projects
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- Immutable per-session project binding. SET NULL only fires on the hard
-- delete path (remote unbind cascade / bot deletion); the API archive path
-- leaves rows in place so bound sessions keep their working directory.
ALTER TABLE public.bot_sessions
    ADD COLUMN IF NOT EXISTS project_id UUID;

ALTER TABLE public.bot_sessions
    DROP CONSTRAINT IF EXISTS bot_sessions_project_id_fkey;
-- NOT VALID skips the validation scan: project_id was added NULL in this
-- very migration, so validation is vacuous — and the scan would evaluate
-- bot_sessions' FORCE RLS policies, which raise when the migration role has
-- no memoh.team_id GUC set. New writes are enforced normally.
ALTER TABLE public.bot_sessions
    ADD CONSTRAINT bot_sessions_project_id_fkey
    FOREIGN KEY (team_id, project_id)
    REFERENCES public.bot_projects(team_id, id) ON DELETE SET NULL (project_id)
    NOT VALID;

-- Sidebar grouping: page a bot's sessions inside one project (or the
-- unassigned bucket) with the same (updated_at, id) keyset the existing
-- session list uses.
CREATE INDEX IF NOT EXISTS idx_bot_sessions_project_active_updated
    ON public.bot_sessions (team_id, bot_id, project_id, updated_at DESC, id DESC)
    WHERE deleted_at IS NULL AND project_id IS NOT NULL;
