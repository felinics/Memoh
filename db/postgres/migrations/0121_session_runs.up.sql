-- 0121_session_runs
-- Durable ledger for admitted session runtime runs. PostgreSQL records only
-- admission, ownership/fencing changes, decisions and terminal transitions;
-- liveness (the owner lease) lives exclusively in the live backend, so there is
-- deliberately no lease_expires_at column and no mid-run checkpoint column.

CREATE TABLE IF NOT EXISTS public.session_runs (
    run_id             UUID        PRIMARY KEY,
    team_id            UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                   REFERENCES public.teams(id) ON DELETE RESTRICT,
    bot_id             UUID        NOT NULL,
    session_id         UUID        NOT NULL,
    -- Caller-supplied retry identity. Unique per session (see the invocation
    -- index below), which is what makes repeated submits idempotent.
    invocation_id      TEXT        NOT NULL,
    -- Canonical turn identity, allocated at admission so a terminal write
    -- targets a pre-decided turn and replay cannot create a second one.
    turn_id            UUID        NOT NULL,
    turn_position      BIGINT      NOT NULL,
    state              TEXT        NOT NULL,
    input_json         JSONB       NOT NULL,
    input_fingerprint  TEXT        NOT NULL,
    -- Ownership is written only when it changes, never on a lease tick.
    owner_id           TEXT,
    fencing_token      BIGINT      NOT NULL DEFAULT 0,
    owner_since        TIMESTAMPTZ,
    -- Live backend incarnation that claimed this run. Stamped once, never
    -- updated, so it stays a stable keyset cursor for the recovery sweep.
    live_generation    TEXT,
    abort_requested_at TIMESTAMPTZ,
    error_code         TEXT,
    error_message      TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT session_runs_team_run_key UNIQUE (team_id, run_id),
    CONSTRAINT session_runs_state_check CHECK (state IN (
        'accepted', 'running', 'waiting_decision',
        'completed', 'aborted', 'failed', 'lost'
    )),
    CONSTRAINT session_runs_fencing_token_check CHECK (fencing_token >= 0),
    CONSTRAINT session_runs_owner_claim_check CHECK ((owner_id IS NULL) = (owner_since IS NULL)),
    CONSTRAINT session_runs_bot_id_fkey
        FOREIGN KEY (team_id, bot_id)
        REFERENCES public.bots(team_id, id) ON DELETE CASCADE,
    CONSTRAINT session_runs_session_id_fkey
        FOREIGN KEY (team_id, session_id)
        REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE
);

-- SR-ADM-001: repeated submits of one invocation land on one admission result.
CREATE UNIQUE INDEX IF NOT EXISTS session_runs_invocation_unique
    ON public.session_runs (team_id, session_id, invocation_id);

-- SR-OWN-001: at most one active run per session. This index is the admission
-- gate rather than a backstop: a second concurrent invocation violates it, and
-- the ledger turns that violation into a stable retryable busy result. SR-OWN-001
-- permits either durable queueing or a busy answer; answering busy is what keeps
-- a queue state, a queue index, and queue promotion out of the design entirely.
CREATE UNIQUE INDEX IF NOT EXISTS session_runs_single_active
    ON public.session_runs (team_id, session_id)
    WHERE state IN ('accepted', 'running', 'waiting_decision');

-- Keyset cursor for the fail-closed generation sweep.
CREATE INDEX IF NOT EXISTS idx_session_runs_recovery
    ON public.session_runs (team_id, live_generation, run_id)
    WHERE state IN ('accepted', 'running', 'waiting_decision');

-- Abandoned admissions: rows leave this index the moment they are claimed.
CREATE INDEX IF NOT EXISTS idx_session_runs_orphan
    ON public.session_runs (team_id, created_at, run_id)
    WHERE state = 'accepted' AND owner_id IS NULL;

ALTER TABLE public.session_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.session_runs FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS session_runs_team_select ON public.session_runs;
DROP POLICY IF EXISTS session_runs_team_insert ON public.session_runs;
DROP POLICY IF EXISTS session_runs_team_update ON public.session_runs;
DROP POLICY IF EXISTS session_runs_team_delete ON public.session_runs;

CREATE POLICY session_runs_team_select ON public.session_runs
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY session_runs_team_insert ON public.session_runs
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY session_runs_team_update ON public.session_runs
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY session_runs_team_delete ON public.session_runs
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- Decision -> run + turn (SR-DEC-001) and history -> run traceability.
-- Deliberately plain nullable columns rather than foreign keys: history and
-- decision writes already take a parent-before-child lock path
-- (LockBotForSessionWrite, LockSessionRuntimeFence), and adding session_runs as
-- a third parent would widen that ordering for a column used only for lookup.
ALTER TABLE public.tool_approval_requests
    ADD COLUMN IF NOT EXISTS run_id UUID,
    ADD COLUMN IF NOT EXISTS turn_id UUID;

ALTER TABLE public.user_input_requests
    ADD COLUMN IF NOT EXISTS run_id UUID,
    ADD COLUMN IF NOT EXISTS turn_id UUID;

ALTER TABLE public.bot_history_messages
    ADD COLUMN IF NOT EXISTS run_id UUID;

CREATE INDEX IF NOT EXISTS idx_tool_approval_run
    ON public.tool_approval_requests (team_id, run_id)
    WHERE run_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_user_input_run
    ON public.user_input_requests (team_id, run_id)
    WHERE run_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_bot_history_messages_run
    ON public.bot_history_messages (team_id, run_id)
    WHERE run_id IS NOT NULL;
