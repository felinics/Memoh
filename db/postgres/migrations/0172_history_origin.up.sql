-- 0172_history_origin
-- Without the 24-hour history window, a session's whole unsummarized backlog
-- reaches its next request. A session from before rolling summaries can hold
-- months of it, far more than any request ever read, and folding it all would
-- take hundreds of summarizer passes. Each such session now gets a history
-- origin: where its context starts. History from before it reaches a request
-- only through the summaries that already cover it, and a notice opens the
-- history to say what was left out.
--
-- This migration places the origin once for every live session. Later only a
-- fork or a bot import places one; a session created after this migration
-- never has one. The origin sits where the newest history that fits the
-- session's budget starts, so the first request after the upgrade fits
-- without compacting.

-- memoh_session_history_budget is the history budget, in tokens, an origin is
-- placed for. Every run records the budget it planned in its context
-- lifecycle (budget_plan.history_budget); the newest positive one among the
-- session's recent runs is used. A session without one, such as one whose
-- runs predate those records or one restored by an import, gets half its
-- model's context window: the session's preferred chat model, else the bot's,
-- capped at the server's default absolute context cap of 200000 tokens. Half
-- leaves the system prompt, tool definitions, output reserve and current
-- request their room in any window. Without a known window it is half the
-- cap.
CREATE OR REPLACE FUNCTION public.memoh_session_history_budget(p_session_id uuid)
RETURNS bigint
LANGUAGE plpgsql
STABLE
SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
  owner_team uuid;
  owner_bot uuid;
  preferred_model uuid;
  planned numeric;
  model_window numeric;
BEGIN
  SELECT session.team_id, session.bot_id, session.preferred_chat_model_id
    INTO owner_team, owner_bot, preferred_model
  FROM public.bot_sessions session
  WHERE session.id = p_session_id;
  IF NOT FOUND THEN
    RETURN NULL;
  END IF;

  SELECT recent.history_budget
    INTO planned
  FROM (
    SELECT lifecycle.created_at,
      lifecycle.run_id,
      CASE WHEN pg_catalog.jsonb_typeof(lifecycle.snapshot->'budget_plan'->'history_budget') = 'number'
        THEN (lifecycle.snapshot->'budget_plan'->>'history_budget')::numeric
      END AS history_budget
    FROM public.context_lifecycles lifecycle
    WHERE lifecycle.team_id = owner_team
      AND lifecycle.session_id = p_session_id
    ORDER BY lifecycle.created_at DESC, lifecycle.run_id DESC
    LIMIT 16
  ) recent
  WHERE recent.history_budget BETWEEN 1 AND 2147483647
  ORDER BY recent.created_at DESC, recent.run_id DESC
  LIMIT 1;
  IF planned IS NOT NULL THEN
    RETURN pg_catalog.floor(planned)::bigint;
  END IF;

  SELECT CASE WHEN pg_catalog.jsonb_typeof(model.config->'context_window') = 'number'
      THEN (model.config->>'context_window')::numeric
    END
    INTO model_window
  FROM public.models model
  WHERE model.team_id = owner_team
    AND model.id = COALESCE(
      preferred_model,
      (SELECT bot.chat_model_id FROM public.bots bot WHERE bot.team_id = owner_team AND bot.id = owner_bot)
    );
  IF model_window IS NULL OR model_window < 1 THEN
    model_window := 200000;
  END IF;
  RETURN pg_catalog.floor(LEAST(model_window, 200000) / 2)::bigint;
END;
$$;

-- memoh_place_history_origin places a session's history origin for a budget
-- in tokens, records what it leaves out for the notice, and returns it: NULL
-- when the session needs none.
--
-- A request's history is every active summary plus the rows from the origin
-- on. The origin is the start of the oldest turn for which that fits in nine
-- tenths of the budget, measured the way the history load measures it:
-- stored content bytes, nothing for passive_sync rows or for rows a usable
-- summary replaced, 4 bytes per token with the provider estimator's 25
-- percent margin. The last tenth is room for what the request adds beside its
-- history (the notice, memory recall, a longer input). The newest turn is
-- always kept: when it alone does not fit, the origin is its start and
-- ordinary compaction folds the rest. When everything fits there is no
-- origin.
--
-- The origin sits on a clean turn boundary: every visible row of an earlier
-- turn was created before it, and every row of a later turn, with the
-- session event the row records, at or after it. One timestamp then splits
-- row paths (created_at) and event paths (received_at_ms) alike. An inbound
-- event is received before the row that records it, so the events of an
-- earlier turn precede the origin as its rows do. Where concurrent turns
-- leave the oldest turn that fits without a clean start, the origin moves to
-- the next newer turn that has one, and when no newer turn has one, to the
-- next older one; a session with none has no origin.
--
-- The work follows what a request keeps, not the session's length: rows are
-- read newest first and the read stops once the budget is spent. Counting
-- the rows the notice reports reads only the visible-history index. The
-- function locks the session row.
CREATE OR REPLACE FUNCTION public.memoh_place_history_origin(p_session_id uuid, p_budget_tokens bigint)
RETURNS timestamptz
LANGUAGE plpgsql
VOLATILE
SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
  owner_team uuid;
  owner_epoch bigint;
  room_bytes bigint;
  raw_from timestamptz;
  spent_bytes bigint := 0;
  walk refcursor;
  step record;
  walk_done boolean;
  open_position bigint;
  open_bytes bigint := 0;
  open_started timestamptz;
  kept_started timestamptz;
  candidate_positions bigint[] := '{}';
  candidate_origins timestamptz[] := '{}';
  snapping_earlier boolean := false;
  tries integer[];
  candidate integer;
  try_position bigint;
  try_origin timestamptz;
  chosen_position bigint;
  chosen_origin timestamptz;
  omitted bigint := 0;
  omitted_from timestamptz;
  omitted_until timestamptz;
BEGIN
  SELECT session.team_id, session.compaction_epoch
    INTO owner_team, owner_epoch
  FROM public.bot_sessions session
  WHERE session.id = p_session_id
  FOR UPDATE;
  IF NOT FOUND THEN
    RETURN NULL;
  END IF;

  -- The rows from the origin on may take nine tenths of the budget, in bytes,
  -- less every active summary as the history load wraps it.
  SELECT GREATEST(COALESCE(p_budget_tokens, 0), 0) * 9 / 10 * 4 * 100 / 125
      - COALESCE(SUM(pg_catalog.octet_length(pg_catalog.btrim(compact.summary, E' \t\n\r\f\x0B'))
          + pg_catalog.octet_length(E'<summary>\n\n</summary>')), 0)
    INTO room_bytes
  FROM public.bot_history_message_compacts compact
  WHERE compact.team_id = owner_team
    AND compact.session_id = p_session_id
    AND compact.compaction_epoch = owner_epoch
    AND compact.status = 'ok'
    AND compact.superseded_by IS NULL
    AND NULLIF(pg_catalog.btrim(compact.summary, E' \t\n\r\f\x0B'), '') IS NOT NULL;

  -- Every row older than the session's compaction anchor is summarized, so
  -- only rows from the anchor on can cost anything.
  SELECT LEAST(
    (
      SELECT message.created_at
      FROM public.bot_history_messages message
      WHERE message.team_id = owner_team
        AND message.session_id = p_session_id
        AND message.compact_id IS NULL
        AND message.turn_visible = true
        AND message.turn_id IS NOT NULL
        AND message.turn_position IS NOT NULL
        AND message.turn_message_seq IS NOT NULL
        AND (message.metadata->>'trigger_mode' IS NULL OR message.metadata->>'trigger_mode' <> 'passive_sync')
      ORDER BY message.created_at ASC
      LIMIT 1
    ),
    (
      SELECT MIN(message.created_at)
      FROM public.bot_history_message_compacts compact
      JOIN public.bot_history_messages message
        ON message.team_id = owner_team
       AND message.compact_id = compact.id
      WHERE compact.team_id = owner_team
        AND compact.session_id = p_session_id
        AND NOT (
          compact.compaction_epoch = owner_epoch
          AND compact.status = 'ok'
          AND NULLIF(pg_catalog.btrim(compact.summary, E' \t\n\r\f\x0B'), '') IS NOT NULL
        )
        AND message.session_id = p_session_id
        AND message.turn_visible = true
        AND message.turn_id IS NOT NULL
        AND message.turn_position IS NOT NULL
        AND message.turn_message_seq IS NOT NULL
        AND (message.metadata->>'trigger_mode' IS NULL OR message.metadata->>'trigger_mode' <> 'passive_sync')
    )
  ) INTO raw_from;

  IF raw_from IS NOT NULL THEN
    OPEN walk FOR
      SELECT CASE
          WHEN folded.id IS NOT NULL
            OR message.role = 'user' AND message.metadata->>'trigger_mode' = 'passive_sync' THEN 0
          ELSE pg_catalog.octet_length(message.content::text)
        END AS content_bytes
      FROM public.bot_history_messages message
      LEFT JOIN public.bot_history_message_compacts folded
        ON folded.team_id = owner_team
       AND folded.id = message.compact_id
       AND folded.bot_id = message.bot_id
       AND folded.session_id = p_session_id
       AND folded.compaction_epoch = owner_epoch
       AND folded.status = 'ok'
       AND NULLIF(pg_catalog.btrim(folded.summary, E' \t\n\r\f\x0B'), '') IS NOT NULL
      WHERE message.team_id = owner_team
        AND message.session_id = p_session_id
        AND message.created_at >= raw_from
        AND message.turn_visible = true
        AND message.turn_id IS NOT NULL
        AND message.turn_position IS NOT NULL
        AND message.turn_message_seq IS NOT NULL
      ORDER BY message.created_at DESC;
    LOOP
      FETCH walk INTO step;
      EXIT WHEN NOT FOUND;
      spent_bytes := spent_bytes + step.content_bytes;
      EXIT WHEN spent_bytes > room_bytes;
    END LOOP;
    CLOSE walk;
  END IF;

  IF spent_bytes > room_bytes THEN
    -- Walk whole turns newest first. Each closed turn is a candidate whose
    -- origin is the earliest start among it and every newer turn. Once a
    -- turn does not fit, the candidates before it are tried oldest first,
    -- then it; once those fail, each older turn as it closes.
    spent_bytes := 0;
    OPEN walk FOR
      SELECT message.turn_position AS step_position,
        CASE
          WHEN folded.id IS NOT NULL
            OR message.role = 'user' AND message.metadata->>'trigger_mode' = 'passive_sync' THEN 0
          ELSE pg_catalog.octet_length(message.content::text)
        END AS content_bytes,
        LEAST(message.created_at, TIMESTAMPTZ 'epoch' + event.received_at_ms * INTERVAL '1 millisecond') AS started_at
      FROM public.bot_history_messages message
      LEFT JOIN public.bot_history_message_compacts folded
        ON folded.team_id = owner_team
       AND folded.id = message.compact_id
       AND folded.bot_id = message.bot_id
       AND folded.session_id = p_session_id
       AND folded.compaction_epoch = owner_epoch
       AND folded.status = 'ok'
       AND NULLIF(pg_catalog.btrim(folded.summary, E' \t\n\r\f\x0B'), '') IS NOT NULL
      LEFT JOIN public.bot_session_events event
        ON event.team_id = owner_team
       AND event.id = message.event_id
      WHERE message.team_id = owner_team
        AND message.session_id = p_session_id
        AND message.turn_visible = true
        AND message.turn_id IS NOT NULL
        AND message.turn_position IS NOT NULL
        AND message.turn_message_seq IS NOT NULL
      ORDER BY message.turn_position DESC, message.turn_message_seq DESC, message.created_at DESC, message.id DESC;
    <<turns>>
    LOOP
      FETCH walk INTO step;
      walk_done := NOT FOUND;
      IF open_position IS NOT NULL AND (walk_done OR step.step_position <> open_position) THEN
        spent_bytes := spent_bytes + open_bytes;
        kept_started := LEAST(kept_started, open_started);
        candidate_positions := candidate_positions || open_position;
        candidate_origins := candidate_origins || pg_catalog.date_trunc('milliseconds', kept_started);
        IF cardinality(candidate_positions) > 1 AND spent_bytes > room_bytes THEN
          IF snapping_earlier THEN
            tries := ARRAY[cardinality(candidate_positions)];
          ELSE
            tries := ARRAY(SELECT pg_catalog.generate_series(cardinality(candidate_positions) - 1, 1, -1))
              || cardinality(candidate_positions);
          END IF;
          FOREACH candidate IN ARRAY tries LOOP
            try_position := candidate_positions[candidate];
            try_origin := candidate_origins[candidate];
            -- A candidate is clean when no row of an earlier turn was created
            -- at or after its origin. Reading the rows from the origin on in
            -- creation order keeps the check to what the request keeps.
            IF (
              SELECT message.id
              FROM public.bot_history_messages message
              WHERE message.team_id = owner_team
                AND message.session_id = p_session_id
                AND message.created_at >= try_origin
                AND message.turn_visible = true
                AND message.turn_id IS NOT NULL
                AND message.turn_position IS NOT NULL
                AND message.turn_message_seq IS NOT NULL
                AND message.turn_position < try_position
              ORDER BY message.created_at ASC
              LIMIT 1
            ) IS NULL THEN
              chosen_position := try_position;
              chosen_origin := try_origin;
              EXIT turns;
            END IF;
          END LOOP;
          snapping_earlier := true;
        END IF;
      END IF;
      EXIT WHEN walk_done;
      IF step.step_position IS DISTINCT FROM open_position THEN
        open_position := step.step_position;
        open_bytes := 0;
        open_started := step.started_at;
      END IF;
      open_bytes := open_bytes + step.content_bytes;
      open_started := LEAST(open_started, step.started_at);
    END LOOP;
    CLOSE walk;
  END IF;

  IF chosen_origin IS NOT NULL THEN
    SELECT count(*), MIN(message.created_at), MAX(message.created_at)
      INTO omitted, omitted_from, omitted_until
    FROM public.bot_history_messages message
    WHERE message.team_id = owner_team
      AND message.session_id = p_session_id
      AND message.turn_visible = true
      AND message.turn_id IS NOT NULL
      AND message.turn_position IS NOT NULL
      AND message.turn_message_seq IS NOT NULL
      AND message.turn_position < chosen_position;
    IF omitted = 0 THEN
      chosen_origin := NULL;
      omitted_from := NULL;
      omitted_until := NULL;
    END IF;
  END IF;

  UPDATE public.bot_sessions session
  SET history_origin_at = chosen_origin,
      history_omitted_count = omitted,
      history_omitted_first_at = omitted_from,
      history_omitted_last_at = omitted_until
  WHERE session.team_id = owner_team
    AND session.id = p_session_id
    AND (session.history_origin_at, session.history_omitted_count, session.history_omitted_first_at, session.history_omitted_last_at)
      IS DISTINCT FROM (chosen_origin, omitted, omitted_from, omitted_until);
  RETURN chosen_origin;
END;
$$;

-- memoh_rewind_compaction moves a session's compaction anchor back before
-- p_from, once history from there on changed (a turn replaced or hidden).
-- A summary that ends before p_from still stands for exactly the rows it
-- covered, so it is carried into the session's next epoch and becomes the
-- active summary again; every later summary, and any compaction still in
-- flight, stays in the old epoch and no longer counts. The rows those later
-- summaries covered read raw again, as they did when the anchor was there, so
-- nothing older than the anchor is ever folded a second time. History before
-- the session's origin reaches a request only through the summaries that
-- cover it: a change there that no usable summary reaches changes nothing a
-- request reads, so it rewinds nothing. A summary's anchors say what it
-- reaches even once the changed row is deleted; one from before anchors
-- reaches the rows that still carry it. The caller holds the session row
-- lock.
CREATE OR REPLACE FUNCTION public.memoh_rewind_compaction(p_session_id uuid, p_from timestamptz)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
  previous_epoch bigint;
  origin timestamptz;
  origin_ms bigint;
  from_ms bigint := pg_catalog.floor(pg_catalog.date_part('epoch', p_from) * 1000)::bigint;
BEGIN
  SELECT session.compaction_epoch, session.history_origin_at
    INTO previous_epoch, origin
  FROM public.bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = p_session_id;
  IF NOT FOUND THEN
    RETURN false;
  END IF;
  origin_ms := pg_catalog.floor(pg_catalog.date_part('epoch', origin) * 1000)::bigint;
  IF p_from < origin AND NOT EXISTS (
    SELECT 1
    FROM public.bot_history_message_compacts compact
    WHERE compact.team_id = public.memoh_current_team_id()
      AND compact.session_id = p_session_id
      AND compact.compaction_epoch = previous_epoch
      AND compact.status = 'ok'
      AND NULLIF(pg_catalog.btrim(compact.summary, E' \t\n\r\f\x0B'), '') IS NOT NULL
      AND CASE
        WHEN compact.anchor_start_ms > 0 AND compact.anchor_start_ms <= compact.anchor_end_ms
          THEN compact.anchor_end_ms >= from_ms AND compact.anchor_start_ms < origin_ms
        ELSE EXISTS (
          SELECT 1
          FROM public.bot_history_messages message
          WHERE message.team_id = public.memoh_current_team_id()
            AND message.compact_id = compact.id
            AND message.session_id = p_session_id
            AND message.created_at >= p_from
            AND message.created_at < origin
        )
      END
  ) THEN
    RETURN false;
  END IF;
  UPDATE public.bot_sessions session
  SET compaction_epoch = session.compaction_epoch + 1
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = p_session_id;
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

-- The origin and what the notice reports about the rows before it: how many
-- visible rows, and when the first and the last was created. They are set
-- together or not at all. Sessions that exist the first time these columns
-- appear get their origin here; on a schema that already has them (a fresh
-- install, whose 0001 creates them, or a repeated run) there is nothing to
-- place.
DO $history_origin$
DECLARE
  live_session uuid;
BEGIN
  IF EXISTS (
    SELECT 1
    FROM pg_catalog.pg_attribute
    WHERE attrelid = 'public.bot_sessions'::regclass
      AND attname = 'history_origin_at'
      AND NOT attisdropped
  ) THEN
    RETURN;
  END IF;

  ALTER TABLE public.bot_sessions
    ADD COLUMN history_origin_at TIMESTAMPTZ,
    ADD COLUMN history_omitted_count BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN history_omitted_first_at TIMESTAMPTZ,
    ADD COLUMN history_omitted_last_at TIMESTAMPTZ,
    ADD CONSTRAINT bot_sessions_history_origin_check CHECK (
      (history_origin_at IS NULL
        AND history_omitted_count = 0
        AND history_omitted_first_at IS NULL
        AND history_omitted_last_at IS NULL)
      OR (history_origin_at IS NOT NULL
        AND history_omitted_count > 0
        AND history_omitted_first_at <= history_omitted_last_at
        AND history_omitted_last_at < history_origin_at)
    );

  -- Every table placement reads or writes is under FORCE row level security
  -- keyed on memoh.team_id, which a migration connection never sets. Lift it
  -- for the backfill and restore it after, as 0165 does.
  ALTER TABLE public.bots NO FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bots DISABLE ROW LEVEL SECURITY;
  ALTER TABLE public.models NO FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.models DISABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_sessions NO FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_sessions DISABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_session_events NO FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_session_events DISABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_messages NO FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_messages DISABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_message_compacts NO FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_message_compacts DISABLE ROW LEVEL SECURITY;
  ALTER TABLE public.context_lifecycles NO FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.context_lifecycles DISABLE ROW LEVEL SECURITY;

  FOR live_session IN
    SELECT session.id
    FROM public.bot_sessions session
    WHERE session.deleted_at IS NULL
    ORDER BY session.id
  LOOP
    PERFORM public.memoh_place_history_origin(live_session, public.memoh_session_history_budget(live_session));
  END LOOP;

  ALTER TABLE public.context_lifecycles ENABLE ROW LEVEL SECURITY;
  ALTER TABLE public.context_lifecycles FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_message_compacts ENABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_message_compacts FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_messages ENABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_messages FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_session_events ENABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_session_events FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_sessions ENABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_sessions FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.models ENABLE ROW LEVEL SECURITY;
  ALTER TABLE public.models FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;
END
$history_origin$;
