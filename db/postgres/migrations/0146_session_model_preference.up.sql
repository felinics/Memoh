-- 0146_session_model_preference
-- Per-session persisted (chat model, reasoning effort) pair (issue #879,
-- spec v2).
--
-- Two columns on bot_sessions, logically one value: the session's current
-- pair. Written only when the pair carries an explicit source: the picker
-- PATCH, the first-send INSERT (createSession body), and the per-turn
-- write-back when the request itself carries the pair and it differs from
-- the stored value. Requests that omit the pair (frontend source=default)
-- never write, so sessions the user never picked on keep following the bot
-- default live. Channel /model and /reasoning CLEAR the pair (fall back to
-- the bot default chain); otherwise channels never write, so untouched
-- channel sessions keep both columns NULL. NULL/NULL = no memory.
--
-- updated_at is deliberately NOT bumped on preference writes: the
-- (bot_id, updated_at DESC) indexes drive sidebar recency, and a picker
-- change or per-turn write-back must not reorder the session list.

ALTER TABLE public.bot_sessions
  ADD COLUMN IF NOT EXISTS preferred_chat_model_id UUID REFERENCES models(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS preferred_reasoning_effort TEXT;

-- Seed query support: "the bot's (per user) most recent native session that
-- has a pair" (welcome composer seed). Existing
-- idx_bot_sessions_bot_mode_runtime_active_updated(bot_id, session_mode,
-- runtime_type, updated_at DESC) already narrows bot_id + mode/runtime +
-- recency; the created_by_user_id + IS NOT NULL pair filter applies on top
-- of a small set.
