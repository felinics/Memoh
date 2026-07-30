-- 0124_workspace_context_snapshots
-- Persist the workspace-derived context used before an Agent model call so
-- ordinary turns do not need to open the workspace runtime.

CREATE TABLE IF NOT EXISTS public.bot_workspace_context_snapshots (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id               UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                      REFERENCES public.teams(id) ON DELETE RESTRICT,
    bot_id                UUID        NOT NULL,
    target_id             TEXT        NOT NULL,
    requested_generation  BIGINT      NOT NULL DEFAULT 0,
    applied_generation    BIGINT      NOT NULL DEFAULT 0,
    status                TEXT        NOT NULL DEFAULT 'empty',
    payload               JSONB,
    content_hash          TEXT,
    last_refresh_error     TEXT,
    refreshed_at          TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT bot_workspace_context_snapshots_target_key
        UNIQUE (team_id, bot_id, target_id),
    CONSTRAINT bot_workspace_context_snapshots_bot_id_fkey
        FOREIGN KEY (team_id, bot_id)
        REFERENCES public.bots(team_id, id) ON DELETE CASCADE,
    CONSTRAINT bot_workspace_context_snapshots_generation_check CHECK (
        requested_generation >= 0
        AND applied_generation >= 0
        AND applied_generation <= requested_generation
    ),
    CONSTRAINT bot_workspace_context_snapshots_status_check CHECK (
        status IN ('empty', 'refreshing', 'ready', 'source_invalid')
    ),
    CONSTRAINT bot_workspace_context_snapshots_payload_check CHECK (
        (payload IS NULL) = (content_hash IS NULL)
    )
);

ALTER TABLE public.bot_workspace_context_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_context_snapshots FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS bot_workspace_context_snapshots_team_select
    ON public.bot_workspace_context_snapshots;
DROP POLICY IF EXISTS bot_workspace_context_snapshots_team_insert
    ON public.bot_workspace_context_snapshots;
DROP POLICY IF EXISTS bot_workspace_context_snapshots_team_update
    ON public.bot_workspace_context_snapshots;
DROP POLICY IF EXISTS bot_workspace_context_snapshots_team_delete
    ON public.bot_workspace_context_snapshots;

CREATE POLICY bot_workspace_context_snapshots_team_select
    ON public.bot_workspace_context_snapshots
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workspace_context_snapshots_team_insert
    ON public.bot_workspace_context_snapshots
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workspace_context_snapshots_team_update
    ON public.bot_workspace_context_snapshots
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workspace_context_snapshots_team_delete
    ON public.bot_workspace_context_snapshots
    FOR DELETE USING (team_id = public.memoh_current_team_id());
