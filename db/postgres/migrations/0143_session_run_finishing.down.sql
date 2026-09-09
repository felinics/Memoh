-- Resolve any prepared outcome before returning to a schema whose runtime does
-- not understand finishing. This preserves the accepted terminal fact instead
-- of turning rollback into owner loss.
UPDATE public.session_runs
SET state = proposed_terminal_state,
    error_code = proposed_error_code,
    error_message = proposed_error_message,
    updated_at = now()
WHERE state = 'finishing'
  AND proposed_terminal_state IN ('completed', 'aborted', 'failed');

ALTER TABLE public.session_runs
    DROP CONSTRAINT IF EXISTS session_runs_finishing_proposal_check;
ALTER TABLE public.session_runs
    DROP CONSTRAINT IF EXISTS session_runs_terminal_proposal_check;
ALTER TABLE public.session_runs
    DROP CONSTRAINT IF EXISTS session_runs_state_check;

ALTER TABLE public.session_runs
    ADD CONSTRAINT session_runs_state_check CHECK (state IN (
        'accepted', 'running', 'waiting_decision',
        'completed', 'aborted', 'failed', 'lost'
    ));

-- Replace the partial indexes only after every finishing row above has become
-- terminal, restoring the pre-0143 admission and recovery predicates.
DROP INDEX IF EXISTS public.session_runs_single_active;
CREATE UNIQUE INDEX session_runs_single_active
    ON public.session_runs (team_id, session_id)
    WHERE state IN ('accepted', 'running', 'waiting_decision');

DROP INDEX IF EXISTS public.idx_session_runs_recovery;
CREATE INDEX idx_session_runs_recovery
    ON public.session_runs (team_id, live_generation, run_id)
    WHERE state IN ('accepted', 'running', 'waiting_decision');

ALTER TABLE public.session_runs
    DROP COLUMN IF EXISTS finish_proposed_at,
    DROP COLUMN IF EXISTS proposed_error_message,
    DROP COLUMN IF EXISTS proposed_error_code,
    DROP COLUMN IF EXISTS proposed_terminal_state;
