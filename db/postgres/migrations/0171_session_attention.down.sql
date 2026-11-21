-- 0171_session_attention
-- Remove session unread state: the read cursors table and the attention
-- columns on bot_sessions.

DROP TABLE IF EXISTS public.session_reads;

DROP INDEX IF EXISTS public.idx_bot_sessions_attention;

ALTER TABLE public.bot_sessions
  DROP CONSTRAINT IF EXISTS bot_sessions_attention_kind_check;

ALTER TABLE public.bot_sessions
  DROP COLUMN IF EXISTS attention_kind,
  DROP COLUMN IF EXISTS attention_at;
