-- 0172_history_origin
-- Remove the history origin: its columns, the origin rule in the compaction
-- rewind, and the placement and budget functions.

ALTER TABLE public.bot_sessions
  DROP CONSTRAINT IF EXISTS bot_sessions_history_origin_check,
  DROP COLUMN IF EXISTS history_omitted_last_at,
  DROP COLUMN IF EXISTS history_omitted_first_at,
  DROP COLUMN IF EXISTS history_omitted_count,
  DROP COLUMN IF EXISTS history_origin_at;

-- memoh_rewind_compaction moves a session's compaction anchor back before
-- p_from, once history from there on changed (a turn replaced or hidden).
-- A summary that ends before p_from still stands for exactly the rows it
-- covered, so it is carried into the session's next epoch and becomes the
-- active summary again; every later summary, and any compaction still in
-- flight, stays in the old epoch and no longer counts. The rows those later
-- summaries covered read raw again, as they did when the anchor was there, so
-- nothing older than the anchor is ever folded a second time. The caller
-- holds the session row lock.
CREATE OR REPLACE FUNCTION public.memoh_rewind_compaction(p_session_id uuid, p_from timestamptz)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
  previous_epoch bigint;
  from_ms bigint := pg_catalog.floor(pg_catalog.date_part('epoch', p_from) * 1000)::bigint;
BEGIN
  UPDATE public.bot_sessions session
  SET compaction_epoch = session.compaction_epoch + 1
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = p_session_id
  RETURNING session.compaction_epoch - 1 INTO previous_epoch;
  IF previous_epoch IS NULL THEN
    RETURN false;
  END IF;
  WITH kept AS MATERIALIZED (
    SELECT compact.id
    FROM public.bot_history_message_compacts compact
    WHERE compact.team_id = public.memoh_current_team_id()
      AND compact.session_id = p_session_id
      AND compact.compaction_epoch = previous_epoch
      AND compact.status = 'ok'
      AND compact.anchor_start_ms > 0
      AND compact.anchor_start_ms <= compact.anchor_end_ms
      AND compact.anchor_end_ms < from_ms
  )
  UPDATE public.bot_history_message_compacts compact
  SET compaction_epoch = previous_epoch + 1,
      superseded_by = CASE WHEN compact.superseded_by IN (SELECT kept.id FROM kept) THEN compact.superseded_by END,
      superseded_at = CASE WHEN compact.superseded_by IN (SELECT kept.id FROM kept) THEN compact.superseded_at END
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.id IN (SELECT kept.id FROM kept);
  RETURN true;
END;
$$;

DROP FUNCTION IF EXISTS public.memoh_place_history_origin(uuid, bigint);
DROP FUNCTION IF EXISTS public.memoh_session_history_budget(uuid);
