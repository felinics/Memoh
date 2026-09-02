-- 0146_context_lifecycle_selection_decisions
-- Move the per-fragment selection decisions out of the lifecycle snapshot into
-- their own column. The snapshot becomes a bounded summary, so list and status
-- readers never detoast the one part that grows with conversation length.
-- Existing rows are split in place, per team, because the table forces row
-- level security. Opening the teams policy takes an exclusive lock on
-- public.teams for the whole migration transaction while every lifecycle row is
-- rewritten twice, so run this with the server stopped, as the compose stack
-- does; on large tables expect it to take minutes.

ALTER TABLE public.context_lifecycles
    ADD COLUMN IF NOT EXISTS selection_decisions JSONB;

-- The per-team loop reads public.teams, whose forced policy needs a bound
-- team; open it for the duration of this migration as 0120 does.
ALTER POLICY teams_self_select ON public.teams USING (true);

DO $$
DECLARE
  previous_team_id text;
  migration_team_id uuid;
BEGIN
  previous_team_id := current_setting('memoh.team_id', true);

  FOR migration_team_id IN SELECT id FROM public.teams ORDER BY id LOOP
    PERFORM set_config('memoh.team_id', migration_team_id::text, true);

    UPDATE public.context_lifecycles
    SET selection_decisions = snapshot -> 'selection_decisions',
        snapshot = snapshot - 'selection_decisions'
    WHERE team_id = migration_team_id
      AND snapshot ? 'selection_decisions';

    -- Rows written before the rollup existed get the same bounded facts new
    -- rows carry, so readers never have to fall back to the audit column.
    UPDATE public.context_lifecycles AS lifecycles
    SET snapshot = jsonb_set(
      lifecycles.snapshot,
      '{selection}',
      COALESCE(lifecycles.snapshot -> 'selection', '{}'::jsonb)
        || jsonb_strip_nulls(jsonb_build_object(
          'trimmed', NULLIF(rollup.trimmed, 0),
          'drop_reason_tokens', rollup.drop_reason_tokens
        ))
    )
    FROM (
      SELECT run_id,
        SUM(trimmed) AS trimmed,
        jsonb_object_agg(reason, tokens) FILTER (WHERE reason IS NOT NULL) AS drop_reason_tokens
      FROM (
        SELECT run_id,
          COUNT(*) FILTER (WHERE decision->>'decision' = 'trimmed') AS trimmed,
          CASE WHEN decision->>'decision' = 'dropped'
            THEN COALESCE(NULLIF(btrim(decision->>'reason'), ''), 'unknown')
          END AS reason,
          SUM(COALESCE((decision->>'token_estimate')::bigint, 0))
            FILTER (WHERE decision->>'decision' = 'dropped') AS tokens
        FROM public.context_lifecycles,
          LATERAL jsonb_array_elements(selection_decisions) AS decision
        WHERE team_id = migration_team_id
          AND jsonb_typeof(selection_decisions) = 'array'
          AND NOT (COALESCE(snapshot -> 'selection', '{}'::jsonb) ? 'drop_reason_tokens')
          AND NOT (COALESCE(snapshot -> 'selection', '{}'::jsonb) ? 'trimmed')
        GROUP BY run_id, 3
      ) AS per_reason
      GROUP BY run_id
    ) AS rollup
    WHERE lifecycles.run_id = rollup.run_id
      AND lifecycles.team_id = migration_team_id
      AND (rollup.trimmed > 0 OR rollup.drop_reason_tokens IS NOT NULL);
  END LOOP;

  PERFORM set_config('memoh.team_id', COALESCE(previous_team_id, ''), true);
END $$;

ALTER POLICY teams_self_select ON public.teams
  USING (id = public.memoh_current_team_id());
