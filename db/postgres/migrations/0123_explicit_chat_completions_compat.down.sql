-- 0123_explicit_chat_completions_compat
-- Remove only compatibility modes written by the 0123 backfill. Preserve a
-- value if it was subsequently changed by the user.

ALTER POLICY teams_self_select ON public.teams USING (true);

DO $$
DECLARE
  migration_team_id UUID;
  previous_team_id TEXT;
  marker_key CONSTANT TEXT := '_memoh_migration_0123_chat_completions_compat';
BEGIN
  previous_team_id := current_setting('memoh.team_id', true);

  FOR migration_team_id IN SELECT id FROM public.teams ORDER BY id LOOP
    PERFORM set_config('memoh.team_id', migration_team_id::text, true);

    UPDATE public.providers
    SET
      config = CASE
        WHEN config ->> 'chat_completions_compat' = metadata ->> marker_key
          THEN config - 'chat_completions_compat'
        ELSE config
      END,
      metadata = metadata - marker_key,
      updated_at = now()
    WHERE metadata ? marker_key;
  END LOOP;

  PERFORM set_config('memoh.team_id', COALESCE(previous_team_id, ''), true);
END
$$;

ALTER POLICY teams_self_select ON public.teams
  USING (id = public.memoh_current_team_id());
