-- 0153_bot_workspaces
-- Declarative workspace state for bots. Each row records what the bot's
-- workspace should be (desired) and what the reconciler last observed, so a
-- failed or interrupted provisioning is never lost and every backend
-- operation is retried by the same loop instead of ad-hoc compensation in the
-- request path. bots.status gains 'failed' for a bot whose workspace never
-- became ready.

BEGIN;

ALTER TABLE public.bots DROP CONSTRAINT IF EXISTS bots_status_check;
ALTER TABLE public.bots ADD CONSTRAINT bots_status_check
    CHECK (status IN ('creating', 'ready', 'deleting', 'failed'));

CREATE TABLE IF NOT EXISTS public.bot_workspaces (
    bot_id              UUID        PRIMARY KEY,
    team_id             UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                    REFERENCES public.teams(id) ON DELETE RESTRICT,
    -- Intent, written only by the API layer.
    desired_state       TEXT        NOT NULL,
    desired_generation  BIGINT      NOT NULL DEFAULT 1,
    image               TEXT        NOT NULL DEFAULT '',
    preserve_data       BOOLEAN     NOT NULL DEFAULT false,
    -- Observation, written only by the reconciler while holding the lease.
    observed_state      TEXT        NOT NULL DEFAULT 'absent',
    observed_generation BIGINT      NOT NULL DEFAULT 0,
    ever_ready          BOOLEAN     NOT NULL DEFAULT false,
    last_error          TEXT        NOT NULL DEFAULT '',
    last_error_phase    TEXT        NOT NULL DEFAULT '',
    attempts            INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_owner         TEXT        NOT NULL DEFAULT '',
    lease_until         TIMESTAMPTZ,
    version             BIGINT      NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT memoh_team_key_7443107a7495 UNIQUE (team_id, bot_id),
    CONSTRAINT bot_workspaces_desired_state_check
        CHECK (desired_state IN ('present', 'absent')),
    CONSTRAINT bot_workspaces_observed_state_check
        CHECK (observed_state IN ('absent', 'provisioning', 'running', 'stopped', 'failed', 'removing')),
    CONSTRAINT bot_workspaces_last_error_phase_check
        CHECK (last_error_phase IN ('', 'image_prepare', 'start', 'bridge', 'bootstrap', 'teardown'))
);

-- bots is under FORCE ROW LEVEL SECURITY; add the reference NOT VALID so the
-- constraint is never validated through the policy-scoped scan.
ALTER TABLE public.bot_workspaces
    DROP CONSTRAINT IF EXISTS bot_workspaces_bot_id_fkey;
ALTER TABLE public.bot_workspaces
    ADD CONSTRAINT bot_workspaces_bot_id_fkey
    FOREIGN KEY (team_id, bot_id)
    REFERENCES public.bots(team_id, id) ON DELETE CASCADE
    NOT VALID;

-- The reconciler claims due rows ordered by next_attempt_at.
CREATE INDEX IF NOT EXISTS idx_bot_workspaces_due
    ON public.bot_workspaces (team_id, next_attempt_at);

ALTER TABLE public.bot_workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspaces FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS bot_workspaces_team_select ON public.bot_workspaces;
DROP POLICY IF EXISTS bot_workspaces_team_insert ON public.bot_workspaces;
DROP POLICY IF EXISTS bot_workspaces_team_update ON public.bot_workspaces;
DROP POLICY IF EXISTS bot_workspaces_team_delete ON public.bot_workspaces;

CREATE POLICY bot_workspaces_team_select ON public.bot_workspaces
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workspaces_team_insert ON public.bot_workspaces
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workspaces_team_update ON public.bot_workspaces
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workspaces_team_delete ON public.bot_workspaces
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- Backfill from the imperative model.
--   * A bot with a container record keeps it: desired present, observed by the
--     record's status, ever_ready so no automated step may ever delete it.
--   * A bot stuck in 'creating' without a record is a provisioning that was
--     lost (image pull timeout, crash): desired present, observed absent, so
--     the reconciler resumes it.
--   * A ready bot without a record either never had a workspace or had it
--     deleted on purpose. Provisioning it automatically would be a surprise,
--     so its intent is absent; the user creates one explicitly.
--
-- The backfill spans every team. Like other cross-team migrations, lift the
-- request-scoped policies (memoh.team_id is not set while migrating, and the
-- migration role need not have BYPASSRLS) and restore FORCE RLS before
-- committing.
ALTER TABLE public.bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.containers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.containers DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspaces NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspaces DISABLE ROW LEVEL SECURITY;

INSERT INTO public.bot_workspaces (
    bot_id, team_id, desired_state, desired_generation, image,
    observed_state, observed_generation, ever_ready, next_attempt_at
)
SELECT
    b.id,
    b.team_id,
    CASE WHEN c.bot_id IS NOT NULL OR b.status = 'creating' THEN 'present' ELSE 'absent' END,
    1,
    COALESCE(c.image, b.metadata->'workspace'->>'image', ''),
    CASE
        WHEN c.bot_id IS NULL THEN 'absent'
        WHEN c.status = 'running' THEN 'running'
        ELSE 'stopped'
    END,
    CASE WHEN c.bot_id IS NOT NULL OR b.status <> 'creating' THEN 1 ELSE 0 END,
    c.bot_id IS NOT NULL,
    now()
FROM public.bots b
LEFT JOIN LATERAL (
    SELECT bot_id, status, image
    FROM public.containers
    WHERE containers.bot_id = b.id
    ORDER BY updated_at DESC
    LIMIT 1
) c ON true
WHERE b.status <> 'deleting'
ON CONFLICT (bot_id) DO NOTHING;

ALTER TABLE public.bot_workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspaces FORCE ROW LEVEL SECURITY;
ALTER TABLE public.containers ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.containers FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;

COMMIT;
