-- 0173_schedule_notify
-- Let a schedule decide when its runs need the owner. notify_policy
-- 'important' (the default, also for existing schedules) raises unread
-- attention only when the run calls notify_user, fails or waits for a
-- decision; 'every_run' also raises it on every completed run.
-- contact_via_channels (default on) keeps the bot free to reach the owner on
-- its social platforms; off adds a note to the trigger prompt that it need not.
-- bot_sessions.attention_kind gains 'update', the attention a run raises when
-- it calls notify_user.

ALTER TABLE public.schedule
  ADD COLUMN IF NOT EXISTS notify_policy TEXT NOT NULL DEFAULT 'important',
  ADD COLUMN IF NOT EXISTS contact_via_channels BOOLEAN NOT NULL DEFAULT true;

ALTER TABLE public.schedule
  DROP CONSTRAINT IF EXISTS schedule_notify_policy_check,
  ADD CONSTRAINT schedule_notify_policy_check
    CHECK (notify_policy IN ('important', 'every_run'));

-- Unread summaries name the schedule a run session belongs to.
CREATE INDEX IF NOT EXISTS idx_schedule_logs_session
  ON public.schedule_logs (session_id)
  WHERE session_id IS NOT NULL;

ALTER TABLE public.bot_sessions
  DROP CONSTRAINT IF EXISTS bot_sessions_attention_kind_check,
  ADD CONSTRAINT bot_sessions_attention_kind_check
    CHECK (attention_kind IS NULL OR attention_kind IN ('reply', 'failed', 'needs_input', 'update'));
