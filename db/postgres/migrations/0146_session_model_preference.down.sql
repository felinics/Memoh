-- 0146_session_model_preference (down)
-- Reverse of the up migration, in reverse order.

ALTER TABLE public.bot_sessions
  DROP COLUMN IF EXISTS model_preference_revision,
  DROP COLUMN IF EXISTS preferred_external_model_id;

ALTER TABLE public.bot_sessions
  DROP COLUMN IF EXISTS preferred_reasoning_effort;

ALTER TABLE public.bot_sessions
  DROP COLUMN IF EXISTS preferred_chat_model_id;
