-- 0146_bot_dependency_installations
-- Record per-bot, per-workspace-target dependency installation intent for the
-- Workspace dependency manager (docs/design/workspace-dependencies.md §7).
-- Rows express what the user asked for; discovery corrects status/source and
-- never deletes a record (WD-STATE-001).

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

CREATE INDEX IF NOT EXISTS idx_bot_dependency_installations_bot
    ON public.bot_dependency_installations (team_id, bot_id, workspace_target_id);

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
