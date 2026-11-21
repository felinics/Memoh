-- 0170_history_checkpoints
-- Restore the compaction overlay the checkpoints replaced: per-row claims,
-- summary coverage and lineage, and compaction epochs; then drop the
-- checkpoints. What a checkpoint holds does not convert back into claims, so
-- every summary is retired: each session that has any moves to an epoch none
-- of them is in, its whole history is raw again, and compaction folds it anew.

ALTER TABLE public.bot_history_message_compacts
  ADD COLUMN IF NOT EXISTS artifact_version INTEGER NOT NULL DEFAULT 1,
  ADD COLUMN IF NOT EXISTS coverage JSONB NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN IF NOT EXISTS anchor_start_ms BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS anchor_end_ms BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS artifact_level INTEGER NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS parent_ids UUID[] NOT NULL DEFAULT '{}'::uuid[],
  ADD COLUMN IF NOT EXISTS superseded_by UUID,
  ADD COLUMN IF NOT EXISTS superseded_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS compaction_epoch BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_compacts_owner_epoch
  ON public.bot_history_message_compacts (team_id, bot_id, session_id, compaction_epoch, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_compacts_active_session
  ON public.bot_history_message_compacts (team_id, session_id, anchor_start_ms, started_at)
  WHERE status = 'ok' AND superseded_at IS NULL;

ALTER TABLE public.bot_history_messages ADD COLUMN IF NOT EXISTS compact_id UUID;
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_compact
  ON public.bot_history_messages (team_id, compact_id);

-- Validating a reference scans its tables, which are under FORCE row level
-- security keyed on memoh.team_id, which a migration connection never sets.
-- Lift it around the validation and restore it after.
DO $compaction_overlay_fkeys$
BEGIN
  ALTER TABLE public.bot_history_message_compacts NO FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_message_compacts DISABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_messages NO FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_messages DISABLE ROW LEVEL SECURITY;
  IF NOT EXISTS (
    SELECT 1
    FROM pg_catalog.pg_constraint
    WHERE conrelid = 'public.bot_history_message_compacts'::regclass
      AND conname = 'bot_history_message_compacts_superseded_by_fkey'
  ) THEN
    ALTER TABLE public.bot_history_message_compacts
      ADD CONSTRAINT bot_history_message_compacts_superseded_by_fkey
      FOREIGN KEY (team_id, superseded_by)
      REFERENCES public.bot_history_message_compacts(team_id, id)
      ON DELETE SET NULL (superseded_by);
  END IF;
  IF NOT EXISTS (
    SELECT 1
    FROM pg_catalog.pg_constraint
    WHERE conrelid = 'public.bot_history_messages'::regclass
      AND conname = 'fk_compact_id'
  ) THEN
    ALTER TABLE public.bot_history_messages
      ADD CONSTRAINT fk_compact_id
      FOREIGN KEY (team_id, compact_id)
      REFERENCES public.bot_history_message_compacts(team_id, id)
      ON DELETE SET NULL (compact_id);
  END IF;
  ALTER TABLE public.bot_history_messages ENABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_messages FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_message_compacts ENABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_message_compacts FORCE ROW LEVEL SECURITY;
END
$compaction_overlay_fkeys$;

DROP VIEW IF EXISTS public.bot_visible_history_messages;
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
  m.compact_id,
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

ALTER TABLE public.bot_sessions ADD COLUMN IF NOT EXISTS compaction_epoch BIGINT NOT NULL DEFAULT 0;

DO $retire_summaries$
BEGIN
  ALTER TABLE public.bot_sessions NO FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_sessions DISABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_message_compacts NO FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_message_compacts DISABLE ROW LEVEL SECURITY;
  UPDATE public.bot_sessions session
  SET compaction_epoch = 1
  WHERE EXISTS (
    SELECT 1
    FROM public.bot_history_message_compacts compact
    WHERE compact.team_id = session.team_id
      AND compact.session_id = session.id
  );
  ALTER TABLE public.bot_history_message_compacts ENABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_history_message_compacts FORCE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_sessions ENABLE ROW LEVEL SECURITY;
  ALTER TABLE public.bot_sessions FORCE ROW LEVEL SECURITY;
END
$retire_summaries$;

DROP FUNCTION IF EXISTS public.memoh_rewind_history_checkpoint(uuid, bigint, bigint);
DROP FUNCTION IF EXISTS public.memoh_history_checkpoint_kept(uuid, bigint, bigint);
ALTER TABLE public.bot_sessions
  DROP CONSTRAINT IF EXISTS bot_sessions_history_checkpoint_id_fkey,
  DROP COLUMN IF EXISTS history_checkpoint_id;
DROP TABLE IF EXISTS public.bot_history_checkpoints;
