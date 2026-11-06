-- 0173_schedule_notify
-- Remove schedule notification settings and the 'update' attention kind. A
-- session whose latest attention was an update keeps it as a plain reply.

-- bot_sessions is under FORCE row level security keyed on memoh.team_id,
-- which a migration connection never sets. Lift it for the data step and
-- restore it after, as 0165 does.
ALTER TABLE public.bot_sessions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions DISABLE ROW LEVEL SECURITY;

UPDATE public.bot_sessions
SET attention_kind = 'reply'
WHERE attention_kind = 'update';

ALTER TABLE public.bot_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions FORCE ROW LEVEL SECURITY;

ALTER TABLE public.bot_sessions
  DROP CONSTRAINT IF EXISTS bot_sessions_attention_kind_check,
  ADD CONSTRAINT bot_sessions_attention_kind_check
    CHECK (attention_kind IS NULL OR attention_kind IN ('reply', 'failed', 'needs_input'));

DROP INDEX IF EXISTS public.idx_schedule_logs_session;

ALTER TABLE public.schedule
  DROP CONSTRAINT IF EXISTS schedule_notify_policy_check;

ALTER TABLE public.schedule
  DROP COLUMN IF EXISTS contact_via_channels,
  DROP COLUMN IF EXISTS notify_policy;
