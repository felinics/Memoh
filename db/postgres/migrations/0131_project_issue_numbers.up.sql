-- 0131_project_issue_numbers
-- Per-project sequential issue numbers (#1, #2, …) — the short handle people
-- use to refer to an issue in conversation.
--
-- The number lives on project_nodes rather than project_issue_details because
-- the uniqueness scope is (project, number) and only project_nodes carries
-- project_id; denormalizing project_id into the detail table just to host an
-- index would be the worse trade.
--
-- Numbers are never reused: allocation reads MAX() across ALL issues of the
-- project including soft-deleted ones, so deleting #3 leaves a permanent hole
-- (GitHub/Linear semantics — a stale reference to #3 must never resolve to a
-- different issue).

ALTER TABLE public.project_nodes
    ADD COLUMN IF NOT EXISTS number INT;

-- project_nodes has FORCE ROW LEVEL SECURITY, and the migration role sets no
-- memoh.team_id GUC — any statement that reads the table would fail with
-- "memoh.team_id is not set". So the backfill walks teams explicitly, setting
-- the GUC per team and restoring it afterwards (the 0120 pattern).
--
-- Enumerating the teams to walk hits the same wall: teams_self_select itself
-- calls memoh_current_team_id(). Relax it for the duration and restore it
-- below — again exactly what 0120 does.
ALTER POLICY teams_self_select ON public.teams USING (true);

DO $$
DECLARE
  migration_team_id UUID;
  previous_team_id TEXT;
BEGIN
  previous_team_id := current_setting('memoh.team_id', true);

  FOR migration_team_id IN SELECT id FROM public.teams ORDER BY id LOOP
    PERFORM set_config('memoh.team_id', migration_team_id::text, true);

    UPDATE public.project_nodes n
    SET number = seq.row_number
    FROM (
        SELECT id, team_id,
               row_number() OVER (PARTITION BY project_id ORDER BY created_at, id) AS row_number
        FROM public.project_nodes
        WHERE team_id = migration_team_id AND type = 'issue'
    ) seq
    WHERE n.id = seq.id AND n.team_id = seq.team_id AND n.number IS NULL;
  END LOOP;

  PERFORM set_config('memoh.team_id', COALESCE(previous_team_id, ''), true);
END $$;

ALTER POLICY teams_self_select ON public.teams
  USING (id = public.memoh_current_team_id());

-- NOT VALID skips the validation scan, which would evaluate the table's RLS
-- policies under the same GUC-less migration role. The backfill above already
-- satisfies the invariant for existing rows; new writes are checked normally.
ALTER TABLE public.project_nodes
    DROP CONSTRAINT IF EXISTS project_nodes_number_check;
ALTER TABLE public.project_nodes
    ADD CONSTRAINT project_nodes_number_check
    CHECK ((type = 'issue') = (number IS NOT NULL))
    NOT VALID;

-- Index builds read the heap directly and are not subject to RLS, so this one
-- needs no GUC dance. Soft-deleted rows stay in the index: their numbers are
-- retired, not freed.
CREATE UNIQUE INDEX IF NOT EXISTS project_nodes_issue_number_unique
    ON public.project_nodes (team_id, project_id, number)
    WHERE type = 'issue';
