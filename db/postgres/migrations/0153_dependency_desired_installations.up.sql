-- 0153_dependency_desired_installations
-- Persist confirmed dependency targets and recoverable repair scheduling.
-- Authorized targets are separate from mutable discovery observations. No legacy
-- observation is backfilled: only a confirmed successful mutation creates one.
CREATE TABLE IF NOT EXISTS public.bot_dependency_desired_installations (
    team_id UUID NOT NULL DEFAULT public.memoh_current_team_id() REFERENCES public.teams(id) ON DELETE RESTRICT,
    bot_id UUID NOT NULL,
    dependency_id TEXT NOT NULL CHECK (dependency_id <> ''),
    desired_revision TEXT NOT NULL CHECK (desired_revision ~ '^[a-f0-9]{32}$'),
    version TEXT NOT NULL CHECK (version <> '' AND version <> 'latest'),
    source_url TEXT NOT NULL CHECK (source_url <> ''),
    registry_id TEXT NOT NULL CHECK (registry_id = 'memoh'),
    definition_revision TEXT NOT NULL CHECK (definition_revision ~ '^[a-f0-9]{64}$'),
    manifest_digest TEXT NOT NULL CHECK (manifest_digest ~ '^sha256:[a-f0-9]{64}$'),
    authorized_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    authorized_by_operation_id TEXT NOT NULL CHECK (authorized_by_operation_id ~ '^[a-f0-9]{32}$'),
    authorized_by_actor TEXT NOT NULL DEFAULT '',
    platform_os TEXT NOT NULL,
    platform_arch TEXT NOT NULL,
    platform_libc TEXT NOT NULL DEFAULT '',
    entrypoints JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(entrypoints) = 'object'),
    installation_id TEXT NOT NULL DEFAULT '',
    payload_path TEXT NOT NULL DEFAULT '',
    store_root TEXT NOT NULL DEFAULT '',
    repair_status TEXT NOT NULL DEFAULT 'ready' CHECK (repair_status IN ('ready','queued','installing','backoff','manual_required')),
    repair_operation_id TEXT NOT NULL DEFAULT '' CHECK (repair_operation_id = '' OR repair_operation_id ~ '^[a-f0-9]{32}$'),
    repair_attempts INTEGER NOT NULL DEFAULT 0 CHECK (repair_attempts >= 0),
    repair_next_attempt_at TIMESTAMPTZ,
    repair_last_error_code TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, bot_id, dependency_id),
    FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_dependency_desired_repair_due
    ON public.bot_dependency_desired_installations (team_id, repair_next_attempt_at, bot_id, dependency_id);
ALTER TABLE public.bot_dependency_desired_installations ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_dependency_desired_installations FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dependency_desired_team ON public.bot_dependency_desired_installations;
CREATE POLICY dependency_desired_team ON public.bot_dependency_desired_installations
    USING (team_id = public.memoh_current_team_id()) WITH CHECK (team_id = public.memoh_current_team_id());

-- This compact audit survives successful receipt cleanup and target removal;
-- it retains authorization identity, never historical payloads or credentials.
CREATE TABLE IF NOT EXISTS public.bot_dependency_authorization_events (
    team_id UUID NOT NULL DEFAULT public.memoh_current_team_id() REFERENCES public.teams(id) ON DELETE RESTRICT,
    bot_id UUID NOT NULL,
    dependency_id TEXT NOT NULL,
    operation_id TEXT NOT NULL CHECK (operation_id ~ '^[a-f0-9]{32}$'),
    action TEXT NOT NULL CHECK (action IN ('authorize','revoke')),
    actor TEXT NOT NULL DEFAULT '',
    version TEXT NOT NULL,
    source_url TEXT NOT NULL,
    registry_id TEXT NOT NULL,
    definition_revision TEXT NOT NULL,
    manifest_digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, bot_id, dependency_id, operation_id, action),
    FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE
);
ALTER TABLE public.bot_dependency_authorization_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_dependency_authorization_events FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dependency_authorization_team ON public.bot_dependency_authorization_events;
CREATE POLICY dependency_authorization_team ON public.bot_dependency_authorization_events
    USING (team_id = public.memoh_current_team_id()) WITH CHECK (team_id = public.memoh_current_team_id());

-- Active scripts retain the exact definition even when the last installed copy
-- was produced by an older publication.
ALTER TABLE public.bot_dependency_installations
    ADD COLUMN IF NOT EXISTS last_operation_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS operation_source_url TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS operation_registry_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS operation_definition_revision TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS operation_intent JSONB CHECK (operation_intent IS NULL OR jsonb_typeof(operation_intent) = 'object');
