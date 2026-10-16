-- 0153_bot_workspaces
-- Drop the declarative workspace state and restore the previous bots status
-- set. Bots left in 'failed' are folded back to 'ready' first so the
-- narrower constraint can be re-added.

BEGIN;

DROP TABLE IF EXISTS public.bot_workspaces;

-- The status fold-back spans every team; lift the request-scoped policies
-- (memoh.team_id is not set while migrating) and restore FORCE RLS after.
ALTER TABLE public.bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots DISABLE ROW LEVEL SECURITY;

UPDATE public.bots SET status = 'ready' WHERE status = 'failed';
ALTER TABLE public.bots DROP CONSTRAINT IF EXISTS bots_status_check;
ALTER TABLE public.bots ADD CONSTRAINT bots_status_check
    CHECK (status IN ('creating', 'ready', 'deleting'));

ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;

COMMIT;
