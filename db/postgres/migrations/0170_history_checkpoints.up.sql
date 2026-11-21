-- 0170_history_checkpoints
-- Persist what stands for a session's history before its context as a chain
-- of checkpoints, in place of the compaction overlay. A checkpoint is a
-- position in the session's turn order plus what represents everything up to
-- it: ordered items, rows kept by reference and summaries. A session without
-- an active checkpoint keeps its whole history as context. Every session keeps
-- its context: its active summaries, with the rows in their span they left
-- raw, become its checkpoint, and the overlay goes: per-row claims
-- (compact_id), summary coverage and lineage, and compaction epochs.

CREATE TABLE IF NOT EXISTS public.bot_history_checkpoints (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id          UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                 REFERENCES public.teams(id) ON DELETE RESTRICT,
    session_id       UUID        NOT NULL,
    parent_id        UUID,
    turn_position    BIGINT      NOT NULL,
    turn_message_seq BIGINT      NOT NULL,
    items            JSONB       NOT NULL DEFAULT '[]'::jsonb,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT memoh_team_key_c3970c8700e1 UNIQUE (team_id, id),
    CONSTRAINT bot_history_checkpoints_position_check CHECK (turn_position >= 0 AND turn_message_seq >= 0),
    CONSTRAINT bot_history_checkpoints_items_check CHECK (pg_catalog.jsonb_typeof(items) = 'array'),
    CONSTRAINT bot_history_checkpoints_summary_check CHECK (pg_catalog.jsonb_path_exists(items, '$[*].summary')),
    CONSTRAINT bot_history_checkpoints_session_id_fkey
        FOREIGN KEY (team_id, session_id)
        REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE,
    CONSTRAINT bot_history_checkpoints_parent_id_fkey
        FOREIGN KEY (team_id, parent_id)
        REFERENCES public.bot_history_checkpoints(team_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_bot_history_checkpoints_session
    ON public.bot_history_checkpoints (team_id, session_id);
CREATE INDEX IF NOT EXISTS idx_bot_history_checkpoints_parent
    ON public.bot_history_checkpoints (team_id, parent_id)
    WHERE parent_id IS NOT NULL;

ALTER TABLE public.bot_history_checkpoints ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_history_checkpoints FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS bot_history_checkpoints_team_select ON public.bot_history_checkpoints;
CREATE POLICY bot_history_checkpoints_team_select ON public.bot_history_checkpoints
    FOR SELECT USING (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS bot_history_checkpoints_team_insert ON public.bot_history_checkpoints;
CREATE POLICY bot_history_checkpoints_team_insert ON public.bot_history_checkpoints
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS bot_history_checkpoints_team_update ON public.bot_history_checkpoints;
CREATE POLICY bot_history_checkpoints_team_update ON public.bot_history_checkpoints
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS bot_history_checkpoints_team_delete ON public.bot_history_checkpoints;
CREATE POLICY bot_history_checkpoints_team_delete ON public.bot_history_checkpoints
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- The session's active checkpoint. NULL: nothing stands for any of its
-- history, so its whole history is context.
ALTER TABLE public.bot_sessions ADD COLUMN IF NOT EXISTS history_checkpoint_id UUID;
-- Validating the reference scans both tables, which are under FORCE row
-- level security keyed on memoh.team_id, which a migration connection never
-- sets. Lift it around the validation and restore it after.
DO $history_checkpoint_fkey$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_catalog.pg_constraint
    WHERE conrelid = 'public.bot_sessions'::regclass
      AND conname = 'bot_sessions_history_checkpoint_id_fkey'
  ) THEN
    ALTER TABLE public.bot_sessions NO FORCE ROW LEVEL SECURITY;
    ALTER TABLE public.bot_sessions DISABLE ROW LEVEL SECURITY;
    ALTER TABLE public.bot_history_checkpoints NO FORCE ROW LEVEL SECURITY;
    ALTER TABLE public.bot_history_checkpoints DISABLE ROW LEVEL SECURITY;
    ALTER TABLE public.bot_sessions
      ADD CONSTRAINT bot_sessions_history_checkpoint_id_fkey
      FOREIGN KEY (team_id, history_checkpoint_id)
      REFERENCES public.bot_history_checkpoints(team_id, id)
      ON DELETE SET NULL (history_checkpoint_id);
    ALTER TABLE public.bot_history_checkpoints ENABLE ROW LEVEL SECURITY;
    ALTER TABLE public.bot_history_checkpoints FORCE ROW LEVEL SECURITY;
    ALTER TABLE public.bot_sessions ENABLE ROW LEVEL SECURITY;
    ALTER TABLE public.bot_sessions FORCE ROW LEVEL SECURITY;
  END IF;
END
$history_checkpoint_fkey$;

-- memoh_history_checkpoint_kept is the newest checkpoint on a session's active
-- chain that still holds once rows from a position on were removed (deleted,
-- hidden or replaced). Its summary may have read any row up to its position,
-- so a checkpoint holds only when it is before the position. NULL when no
-- checkpoint holds.
CREATE OR REPLACE FUNCTION public.memoh_history_checkpoint_kept(
  p_session_id uuid,
  p_turn_position bigint,
  p_turn_message_seq bigint
)
RETURNS uuid
LANGUAGE sql
STABLE
SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $$
  WITH RECURSIVE chain AS (
    SELECT checkpoint.id, checkpoint.parent_id, checkpoint.turn_position, checkpoint.turn_message_seq,
      0 AS depth
    FROM public.bot_sessions session
    JOIN public.bot_history_checkpoints checkpoint
      ON checkpoint.team_id = session.team_id
     AND checkpoint.id = session.history_checkpoint_id
    WHERE session.team_id = public.memoh_current_team_id()
      AND session.id = p_session_id
    UNION ALL
    SELECT parent.id, parent.parent_id, parent.turn_position, parent.turn_message_seq,
      chain.depth + 1
    FROM chain
    JOIN public.bot_history_checkpoints parent
      ON parent.team_id = public.memoh_current_team_id()
     AND parent.id = chain.parent_id
  )
  SELECT chain.id
  FROM chain
  WHERE (chain.turn_position, chain.turn_message_seq) < (p_turn_position, p_turn_message_seq)
  ORDER BY chain.depth
  LIMIT 1
$$;

-- memoh_rewind_history_checkpoint makes the newest checkpoint that still
-- holds once rows of the session from a position on were removed, as
-- memoh_history_checkpoint_kept reads the removal, the session's active one,
-- and reports whether that moved it. The checkpoints it moves back past
-- account for a removed row, so none holds again, and nothing else names
-- them: a fork copies its own. It deletes them. The caller holds the session
-- row lock.
CREATE OR REPLACE FUNCTION public.memoh_rewind_history_checkpoint(
  p_session_id uuid,
  p_turn_position bigint,
  p_turn_message_seq bigint
)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
  active uuid;
  kept uuid;
BEGIN
  SELECT session.history_checkpoint_id
    INTO active
  FROM public.bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = p_session_id;
  IF active IS NULL THEN
    RETURN false;
  END IF;
  kept := public.memoh_history_checkpoint_kept(p_session_id, p_turn_position, p_turn_message_seq);
  IF kept IS NOT DISTINCT FROM active THEN
    RETURN false;
  END IF;
  UPDATE public.bot_sessions session
  SET history_checkpoint_id = kept
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = p_session_id;
  -- The first checkpoint after the kept one on the chain; deleting it
  -- deletes the rest through parent_id.
  DELETE FROM public.bot_history_checkpoints checkpoint
  WHERE checkpoint.team_id = public.memoh_current_team_id()
    AND checkpoint.id = (
      WITH RECURSIVE chain AS (
        SELECT moved.id, moved.parent_id
        FROM public.bot_history_checkpoints moved
        WHERE moved.team_id = public.memoh_current_team_id()
          AND moved.id = active
        UNION ALL
        SELECT parent.id, parent.parent_id
        FROM chain
        JOIN public.bot_history_checkpoints parent
          ON parent.team_id = public.memoh_current_team_id()
         AND parent.id = chain.parent_id
        WHERE chain.parent_id IS DISTINCT FROM kept
      )
      SELECT chain.id
      FROM chain
      WHERE chain.parent_id IS NOT DISTINCT FROM kept
      LIMIT 1
    );
  RETURN true;
END;
$$;

-- Convert each session's overlay. Only a schema that still has it holds any:
-- a fresh install builds the final schema in 0001 and the earlier
-- migrations it replays write no rows.
DO $history_checkpoints$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_catalog.pg_attribute
    WHERE attrelid = 'public.bot_history_messages'::regclass
      AND attname = 'compact_id'
      AND NOT attisdropped
  ) THEN
    RETURN;
  END IF;

  -- Every table the conversion reads or writes is under FORCE row level
  -- security keyed on memoh.team_id, which a migration connection never
  -- sets. Lift it for the conversion and restore it after, as 0165 does.
  ALTER TABLE public.bot_sessions NO FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_sessions DISABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_messages NO FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_messages DISABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_message_compacts NO FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_message_compacts DISABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_checkpoints NO FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_checkpoints DISABLE ROW LEVEL SECURITY;

  -- The summaries a request renders: ok, non-blank and in the session's
  -- current epoch. Each resolves to the active summary at the end of its
  -- supersede chain, the one the history load renders for its rows.
  CREATE TEMP TABLE _usable_summaries ON COMMIT DROP AS
  WITH RECURSIVE usable AS MATERIALIZED (
    SELECT compact.id, compact.team_id, compact.session_id, compact.superseded_by, compact.started_at
    FROM public.bot_history_message_compacts compact
    JOIN public.bot_sessions session
      ON session.team_id = compact.team_id
     AND session.id = compact.session_id
     AND session.bot_id = compact.bot_id
     AND session.compaction_epoch = compact.compaction_epoch
    WHERE compact.status = 'ok'
      AND NULLIF(pg_catalog.btrim(compact.summary, E' \t\n\r\f\x0B'), '') IS NOT NULL
  ), resolved AS (
    SELECT usable.id, usable.id AS active_id
    FROM usable
    WHERE usable.superseded_by IS NULL
       OR NOT EXISTS (SELECT 1 FROM usable successor WHERE successor.id = usable.superseded_by)
    UNION ALL
    SELECT parent.id, resolved.active_id
    FROM usable parent
    JOIN resolved ON parent.superseded_by = resolved.id
  )
  SELECT usable.id, usable.team_id, usable.session_id, resolved.active_id, active.started_at AS active_started_at
  FROM usable
  JOIN resolved ON resolved.id = usable.id
  JOIN usable active ON active.id = resolved.active_id;

  -- The context rows a usable summary replaces, by the active summary that
  -- renders them.
  CREATE TEMP TABLE _folded_rows ON COMMIT DROP AS
  SELECT message.id, message.team_id, message.session_id, message.turn_position, message.turn_message_seq,
    summary.active_id
  FROM public.bot_history_messages message
  JOIN _usable_summaries summary
    ON summary.team_id = message.team_id
   AND summary.id = message.compact_id
   AND summary.session_id = message.session_id
  WHERE message.turn_visible = true
    AND message.turn_id IS NOT NULL
    AND message.turn_position IS NOT NULL
    AND message.turn_message_seq IS NOT NULL
    AND (message.metadata->>'trigger_mode' IS NULL OR message.metadata->>'trigger_mode' <> 'passive_sync');
  CREATE INDEX ON _folded_rows (id);
  CREATE INDEX ON _folded_rows (session_id, turn_position DESC, turn_message_seq DESC);
  CREATE INDEX ON _folded_rows (active_id, turn_position DESC, turn_message_seq DESC);
  ANALYZE _usable_summaries;
  ANALYZE _folded_rows;

  -- The active summaries stand for every row up to the last one they
  -- replace. The rows in that span no summary replaces stay raw where they
  -- are: each summary follows the last row it replaces, and a summary none of
  -- whose rows is still visible goes first.
  WITH positioned AS (
    SELECT session.team_id, session.id AS session_id,
      COALESCE(last_folded.turn_position, 0) AS turn_position,
      COALESCE(last_folded.turn_message_seq, 0) AS turn_message_seq
    FROM public.bot_sessions session
    LEFT JOIN LATERAL (
      SELECT folded.turn_position, folded.turn_message_seq
      FROM _folded_rows folded
      WHERE folded.session_id = session.id
      ORDER BY folded.turn_position DESC, folded.turn_message_seq DESC
      LIMIT 1
    ) last_folded ON true
    WHERE EXISTS (SELECT 1 FROM _usable_summaries summary WHERE summary.session_id = session.id)
  ), placed AS (
    SELECT active.session_id, active.active_id AS id,
      COALESCE(last_folded.turn_position, 0) AS turn_position,
      COALESCE(last_folded.turn_message_seq, 0) AS turn_message_seq,
      1 AS kind_order, active.active_started_at AS started_at
    FROM (SELECT DISTINCT session_id, active_id, active_started_at FROM _usable_summaries) active
    LEFT JOIN LATERAL (
      SELECT folded.turn_position, folded.turn_message_seq
      FROM _folded_rows folded
      WHERE folded.active_id = active.active_id
      ORDER BY folded.turn_position DESC, folded.turn_message_seq DESC
      LIMIT 1
    ) last_folded ON true
  ), kept AS (
    SELECT positioned.session_id, message.id, message.turn_position, message.turn_message_seq,
      0 AS kind_order, message.created_at AS started_at
    FROM positioned
    JOIN public.bot_history_messages message
      ON message.team_id = positioned.team_id
     AND message.session_id = positioned.session_id
    WHERE message.turn_visible = true
      AND message.turn_id IS NOT NULL
      AND message.turn_position IS NOT NULL
      AND message.turn_message_seq IS NOT NULL
      AND (message.metadata->>'trigger_mode' IS NULL OR message.metadata->>'trigger_mode' <> 'passive_sync')
      AND (message.turn_position, message.turn_message_seq) <= (positioned.turn_position, positioned.turn_message_seq)
      AND NOT EXISTS (SELECT 1 FROM _folded_rows folded WHERE folded.id = message.id)
  ), items AS (
    SELECT positioned.session_id,
      pg_catalog.jsonb_agg(
        CASE WHEN entry.kind_order = 0
          THEN pg_catalog.jsonb_build_object('row', entry.id)
          ELSE pg_catalog.jsonb_build_object('summary', entry.id)
        END
        ORDER BY entry.turn_position, entry.turn_message_seq, entry.kind_order, entry.started_at, entry.id
      ) AS items
    FROM positioned
    JOIN (
      SELECT session_id, id, turn_position, turn_message_seq, kind_order, started_at FROM placed
      UNION ALL
      SELECT session_id, id, turn_position, turn_message_seq, kind_order, started_at FROM kept
    ) entry ON entry.session_id = positioned.session_id
    GROUP BY positioned.session_id
  ), created AS (
    INSERT INTO public.bot_history_checkpoints (
      team_id, session_id, turn_position, turn_message_seq, items, created_at
    )
    SELECT positioned.team_id, positioned.session_id,
      positioned.turn_position, positioned.turn_message_seq, items.items, now()
    FROM positioned
    JOIN items ON items.session_id = positioned.session_id
    RETURNING id, session_id
  )
  UPDATE public.bot_sessions session
  SET history_checkpoint_id = created.id
  FROM created
  WHERE session.id = created.session_id;

  ALTER TABLE public.bot_history_checkpoints ENABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_checkpoints FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_message_compacts ENABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_message_compacts FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_messages ENABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_messages FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_sessions ENABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_sessions FORCE ROW LEVEL SECURITY;
END
$history_checkpoints$;

-- The overlay the checkpoints replace. The visible-history view projects the
-- per-row claim, so it is rebuilt without it.
DROP VIEW IF EXISTS public.bot_visible_history_messages;

ALTER TABLE public.bot_history_messages DROP CONSTRAINT IF EXISTS fk_compact_id;
DROP INDEX IF EXISTS public.idx_bot_history_messages_compact;
ALTER TABLE public.bot_history_messages DROP COLUMN IF EXISTS compact_id;

CREATE VIEW public.bot_visible_history_messages
WITH (security_invoker = true) AS
SELECT
  m.team_id,
  m.turn_id,
  m.turn_position,
  m.turn_message_seq,
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id,
  m.source_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.model_id,
  m.event_id,
  m.display_text,
  m.created_at
FROM public.bot_history_messages m
WHERE m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL;

ALTER TABLE public.bot_history_message_compacts DROP CONSTRAINT IF EXISTS bot_history_message_compacts_superseded_by_fkey;
DROP INDEX IF EXISTS public.idx_compacts_owner_epoch;
DROP INDEX IF EXISTS public.idx_compacts_active_session;
ALTER TABLE public.bot_history_message_compacts
  DROP COLUMN IF EXISTS artifact_version,
  DROP COLUMN IF EXISTS coverage,
  DROP COLUMN IF EXISTS anchor_start_ms,
  DROP COLUMN IF EXISTS anchor_end_ms,
  DROP COLUMN IF EXISTS artifact_level,
  DROP COLUMN IF EXISTS parent_ids,
  DROP COLUMN IF EXISTS superseded_by,
  DROP COLUMN IF EXISTS superseded_at,
  DROP COLUMN IF EXISTS compaction_epoch;

ALTER TABLE public.bot_sessions DROP COLUMN IF EXISTS compaction_epoch;
