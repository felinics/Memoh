-- 0142_session_input_queues
-- Add durable, independent steer and follow-up queues for session runtime.
CREATE TABLE IF NOT EXISTS public.session_steer_queue (
    item_id UUID PRIMARY KEY DEFAULT gen_random_uuid(), team_id UUID NOT NULL DEFAULT public.memoh_current_team_id() REFERENCES public.teams(id) ON DELETE CASCADE,
    bot_id UUID NOT NULL, session_id UUID NOT NULL, target_run_id UUID NOT NULL, invocation_id TEXT NOT NULL,
    payload JSONB NOT NULL, status TEXT NOT NULL DEFAULT 'accepted', position BIGINT NOT NULL,
    claim_run_id UUID, claim_owner_id TEXT, claim_fencing_token BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT session_steer_queue_status_check CHECK (status IN ('accepted','claimed','applied','rejected','expired','canceled')),
    CONSTRAINT session_steer_queue_claim_check CHECK ((status = 'claimed') = (claim_run_id IS NOT NULL AND claim_owner_id IS NOT NULL AND claim_fencing_token IS NOT NULL)),
    CONSTRAINT session_steer_queue_run_fkey FOREIGN KEY (team_id, target_run_id) REFERENCES public.session_runs(team_id, run_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX IF NOT EXISTS session_steer_queue_invocation_unique ON public.session_steer_queue(team_id, session_id, invocation_id);
CREATE UNIQUE INDEX IF NOT EXISTS session_steer_queue_team_item_unique ON public.session_steer_queue(team_id, item_id);
CREATE INDEX IF NOT EXISTS session_steer_queue_pending_order ON public.session_steer_queue(team_id, session_id, position) WHERE status = 'accepted';
CREATE TABLE IF NOT EXISTS public.session_follow_up_queue (
    item_id UUID PRIMARY KEY DEFAULT gen_random_uuid(), team_id UUID NOT NULL DEFAULT public.memoh_current_team_id() REFERENCES public.teams(id) ON DELETE CASCADE,
    bot_id UUID NOT NULL, session_id UUID NOT NULL, enqueued_during_run_id UUID NOT NULL, assigned_run_id UUID,
    invocation_id TEXT NOT NULL, payload JSONB NOT NULL, status TEXT NOT NULL DEFAULT 'accepted', position BIGINT NOT NULL,
    claim_run_id UUID, claim_owner_id TEXT, claim_fencing_token BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT session_follow_up_queue_status_check CHECK (status IN ('accepted','claimed','applied','rejected','expired','canceled')),
    CONSTRAINT session_follow_up_queue_claim_check CHECK ((status = 'claimed') = (claim_run_id IS NOT NULL AND claim_owner_id IS NOT NULL AND claim_fencing_token IS NOT NULL)),
    CONSTRAINT session_follow_up_queue_run_fkey FOREIGN KEY (team_id, enqueued_during_run_id) REFERENCES public.session_runs(team_id, run_id) ON DELETE CASCADE,
    CONSTRAINT session_follow_up_queue_assigned_run_fkey FOREIGN KEY (team_id, assigned_run_id) REFERENCES public.session_runs(team_id, run_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX IF NOT EXISTS session_follow_up_queue_invocation_unique ON public.session_follow_up_queue(team_id, session_id, invocation_id);
CREATE UNIQUE INDEX IF NOT EXISTS session_follow_up_queue_team_item_unique ON public.session_follow_up_queue(team_id, item_id);
CREATE INDEX IF NOT EXISTS session_follow_up_queue_pending_order ON public.session_follow_up_queue(team_id, session_id, position) WHERE status = 'accepted' AND assigned_run_id IS NULL;
CREATE TABLE IF NOT EXISTS public.session_queue_step_commits (
    team_id UUID NOT NULL DEFAULT public.memoh_current_team_id() REFERENCES public.teams(id) ON DELETE CASCADE,
    run_id UUID NOT NULL,
    step_index BIGINT NOT NULL,
    commit_hash TEXT NOT NULL,
    action TEXT NOT NULL,
    steer_item_id UUID,
    follow_up_item_id UUID,
    continuation_run_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, run_id, step_index),
    CONSTRAINT session_queue_step_commits_run_fkey FOREIGN KEY (team_id, run_id) REFERENCES public.session_runs(team_id, run_id) ON DELETE CASCADE,
    CONSTRAINT session_queue_step_commits_action_check CHECK (action IN ('continue','continue_with_steer','park_decision','start_continuation','stop_current')),
    CONSTRAINT session_queue_step_commits_result_check CHECK (
        (action = 'continue_with_steer' AND steer_item_id IS NOT NULL AND follow_up_item_id IS NULL AND continuation_run_id IS NULL)
        OR (action = 'start_continuation' AND steer_item_id IS NULL AND follow_up_item_id IS NOT NULL AND continuation_run_id IS NOT NULL)
        OR (action IN ('continue','park_decision','stop_current') AND steer_item_id IS NULL AND follow_up_item_id IS NULL AND continuation_run_id IS NULL)
    )
);
ALTER TABLE public.session_steer_queue ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.session_steer_queue FORCE ROW LEVEL SECURITY;
ALTER TABLE public.session_follow_up_queue ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.session_follow_up_queue FORCE ROW LEVEL SECURITY;
ALTER TABLE public.session_queue_step_commits ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.session_queue_step_commits FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS session_steer_queue_team_all ON public.session_steer_queue;
DROP POLICY IF EXISTS session_follow_up_queue_team_all ON public.session_follow_up_queue;
CREATE POLICY session_steer_queue_team_all ON public.session_steer_queue USING (team_id = public.memoh_current_team_id()) WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY session_follow_up_queue_team_all ON public.session_follow_up_queue USING (team_id = public.memoh_current_team_id()) WITH CHECK (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS session_queue_step_commits_team_all ON public.session_queue_step_commits;
CREATE POLICY session_queue_step_commits_team_all ON public.session_queue_step_commits USING (team_id = public.memoh_current_team_id()) WITH CHECK (team_id = public.memoh_current_team_id());
ALTER TABLE public.session_runs ADD COLUMN IF NOT EXISTS source_follow_up_item_id UUID;
CREATE UNIQUE INDEX IF NOT EXISTS session_runs_source_follow_up_unique ON public.session_runs(team_id, source_follow_up_item_id) WHERE source_follow_up_item_id IS NOT NULL;
ALTER TABLE public.session_runs DROP CONSTRAINT IF EXISTS session_runs_source_follow_up_item_fkey;
-- Avoid validating FORCE RLS tables without a team context for restricted migration roles.
ALTER TABLE public.session_runs ADD CONSTRAINT session_runs_source_follow_up_item_fkey FOREIGN KEY (team_id, source_follow_up_item_id) REFERENCES public.session_follow_up_queue(team_id, item_id) ON DELETE RESTRICT NOT VALID;
