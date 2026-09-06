-- 0146_session_model_preference (down)
-- Dropping the columns also drops the FK constraint.
ALTER TABLE public.bot_sessions
  DROP COLUMN IF EXISTS model_preference_revision,
  DROP COLUMN IF EXISTS preferred_external_model_id,
  DROP COLUMN IF EXISTS preferred_reasoning_effort,
  DROP COLUMN IF EXISTS preferred_chat_model_id;
