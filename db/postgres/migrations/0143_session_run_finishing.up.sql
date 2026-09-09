-- 0143_session_run_finishing
--
-- A native terminal event is only a proposal until the fenced durable terminal
-- write lands. Keep that proposal in PostgreSQL so an owner crash between the
-- event and live-state cleanup can be completed by the reaper instead of being
-- misclassified as owner loss.

ALTER TABLE public.session_runs
    ADD COLUMN IF NOT EXISTS proposed_terminal_state TEXT,
    ADD COLUMN IF NOT EXISTS proposed_error_code TEXT,
    ADD COLUMN IF NOT EXISTS proposed_error_message TEXT,
    ADD COLUMN IF NOT EXISTS finish_proposed_at TIMESTAMPTZ;

ALTER TABLE public.session_runs
    DROP CONSTRAINT IF EXISTS session_runs_state_check;

ALTER TABLE public.session_runs
    ADD CONSTRAINT session_runs_state_check CHECK (state IN (
        'accepted', 'running', 'waiting_decision', 'finishing',
        'completed', 'aborted', 'failed', 'lost'
    ));

ALTER TABLE public.session_runs
    DROP CONSTRAINT IF EXISTS session_runs_terminal_proposal_check;

ALTER TABLE public.session_runs
    ADD CONSTRAINT session_runs_terminal_proposal_check CHECK (
        proposed_terminal_state IS NULL
        OR proposed_terminal_state IN ('completed', 'aborted', 'failed')
    );

ALTER TABLE public.session_runs
    DROP CONSTRAINT IF EXISTS session_runs_finishing_proposal_check;

ALTER TABLE public.session_runs
    ADD CONSTRAINT session_runs_finishing_proposal_check CHECK (
        state <> 'finishing'
        OR (proposed_terminal_state IS NOT NULL AND finish_proposed_at IS NOT NULL)
    );

-- Migrations run before the server starts, so replace both partial indexes in
-- this schema transaction instead of spreading one logical change across
-- concurrent build/swap versions.
DROP INDEX IF EXISTS public.session_runs_single_active;
CREATE UNIQUE INDEX IF NOT EXISTS session_runs_single_active
    ON public.session_runs (team_id, session_id)
    WHERE state IN ('accepted', 'running', 'waiting_decision', 'finishing');

DROP INDEX IF EXISTS public.idx_session_runs_recovery;
CREATE INDEX IF NOT EXISTS idx_session_runs_recovery
    ON public.session_runs (team_id, live_generation, run_id)
    WHERE state IN ('accepted', 'running', 'waiting_decision', 'finishing');
