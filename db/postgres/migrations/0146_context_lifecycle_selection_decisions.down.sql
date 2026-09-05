-- 0146_context_lifecycle_selection_decisions
-- Fold the selection decisions back into the snapshot and drop the column.

-- The per-team loop reads public.teams, whose forced policy needs a bound
-- team; open it for the duration of this migration as 0120 does.
ALTER POLICY teams_self_select ON public.teams USING (true);

DO $$
DECLARE
  previous_team_id text;
  migration_team_id uuid;
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'context_lifecycles'
      AND column_name = 'selection_decisions'
  ) THEN
    RETURN;
  END IF;

  previous_team_id := current_setting('memoh.team_id', true);

  FOR migration_team_id IN SELECT id FROM public.teams ORDER BY id LOOP
    PERFORM set_config('memoh.team_id', migration_team_id::text, true);

    UPDATE public.context_lifecycles
    SET snapshot = snapshot || jsonb_build_object('selection_decisions', selection_decisions)
    WHERE team_id = migration_team_id
      AND selection_decisions IS NOT NULL;

    UPDATE public.context_lifecycles
    SET snapshot = jsonb_set(snapshot, '{selection}', (snapshot -> 'selection') - 'trimmed' - 'drop_reason_tokens')
    WHERE team_id = migration_team_id
      AND jsonb_typeof(snapshot -> 'selection') = 'object';
  END LOOP;

  PERFORM set_config('memoh.team_id', COALESCE(previous_team_id, ''), true);
END $$;

ALTER POLICY teams_self_select ON public.teams
  USING (id = public.memoh_current_team_id());

ALTER TABLE public.context_lifecycles
    DROP COLUMN IF EXISTS selection_decisions;
