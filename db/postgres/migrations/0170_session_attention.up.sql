-- 0170_session_attention
-- Unread state for user-facing sessions. bot_sessions gains the moment a
-- session last needed its creator's attention (a run finished, failed or is
-- waiting for a decision) and the kind of that attention; session_reads keeps
-- each user's read cursor per session. A session is unread for a user when
-- attention_at is later than that user's read_at.
--
-- Both columns are born NULL, so existing sessions start read. Writing them
-- must never touch updated_at: sidebar recency follows messages, not this
-- bookkeeping.

ALTER TABLE public.bot_sessions
  ADD COLUMN IF NOT EXISTS attention_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS attention_kind TEXT;

ALTER TABLE public.bot_sessions
  DROP CONSTRAINT IF EXISTS bot_sessions_attention_kind_check,
  ADD CONSTRAINT bot_sessions_attention_kind_check
    CHECK (attention_kind IS NULL OR attention_kind IN ('reply', 'failed', 'needs_input'));

CREATE INDEX IF NOT EXISTS idx_bot_sessions_attention
  ON public.bot_sessions (team_id, created_by_user_id, attention_at DESC)
  WHERE attention_at IS NOT NULL AND deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS public.session_reads (
  team_id    UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                         REFERENCES public.teams(id) ON DELETE RESTRICT,
  user_id    UUID        NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  session_id UUID        NOT NULL,
  read_at    TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (team_id, user_id, session_id)
);

ALTER TABLE public.session_reads
  DROP CONSTRAINT IF EXISTS session_reads_session_id_fkey,
  ADD CONSTRAINT session_reads_session_id_fkey
    FOREIGN KEY (team_id, session_id)
    REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE
    NOT VALID;

CREATE INDEX IF NOT EXISTS idx_session_reads_session
  ON public.session_reads (team_id, session_id);

ALTER TABLE public.session_reads ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.session_reads FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS session_reads_team_select ON public.session_reads;
DROP POLICY IF EXISTS session_reads_team_insert ON public.session_reads;
DROP POLICY IF EXISTS session_reads_team_update ON public.session_reads;
DROP POLICY IF EXISTS session_reads_team_delete ON public.session_reads;

CREATE POLICY session_reads_team_select ON public.session_reads
  FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY session_reads_team_insert ON public.session_reads
  FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY session_reads_team_update ON public.session_reads
  FOR UPDATE
  USING (team_id = public.memoh_current_team_id())
  WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY session_reads_team_delete ON public.session_reads
  FOR DELETE USING (team_id = public.memoh_current_team_id());
