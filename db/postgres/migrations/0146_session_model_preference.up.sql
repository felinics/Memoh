-- 0146_session_model_preference
-- Per-session (model, reasoning effort) pair for native and direct runtimes,
-- plus an optimistic-concurrency revision. NULL = the session never picked.
-- Preference writes never bump updated_at: the (bot_id, updated_at DESC)
-- indexes drive sidebar recency and a picker change must not reorder it.
--
-- FK form follows 0141/0142: post-team composite (team_id, col) with the
-- column list on SET NULL, and NOT VALID because post-team migrations run
-- under FORCE RLS without memoh.team_id (see 0141 for the full rationale).
-- The column is born NULL, so nothing is skipped by NOT VALID.
ALTER TABLE public.bot_sessions
  ADD COLUMN IF NOT EXISTS preferred_chat_model_id UUID,
  ADD COLUMN IF NOT EXISTS preferred_reasoning_effort TEXT,
  ADD COLUMN IF NOT EXISTS preferred_external_model_id TEXT,
  ADD COLUMN IF NOT EXISTS model_preference_revision UUID;

ALTER TABLE public.bot_sessions
  DROP CONSTRAINT IF EXISTS bot_sessions_preferred_chat_model_id_fkey,
  ADD CONSTRAINT bot_sessions_preferred_chat_model_id_fkey
    FOREIGN KEY (team_id, preferred_chat_model_id)
    REFERENCES public.models(team_id, id)
    ON DELETE SET NULL (preferred_chat_model_id)
    NOT VALID;

-- No new index: the welcome-seed query is served by the existing
-- idx_bot_sessions_bot_mode_runtime_active_updated.
