-- 0146_session_model_preference
-- Persist native or direct-runtime model/effort preferences and an optimistic
-- concurrency revision. Unselected sessions retain NULL preferences. Explicit
-- sends always rotate the revision to fence older picker PATCH requests.
--
-- updated_at is deliberately NOT bumped on preference writes: the
-- (bot_id, updated_at DESC) indexes drive sidebar recency, and a picker
-- change or per-turn write-back must not reorder the session list.

-- This migration runs AFTER the team core: new FKs must be written in the
-- post-team composite form (team_id, col) -> (team_id, col) with the column
-- list on SET NULL. The plain inline form would leave confdelsetcols NULL,
-- which the team schema guards reject (a bare SET NULL could clear team_id).
-- 0001 needs no such care: its team phase rewrites every single-column FK
-- into this exact shape generically, so the fresh and incremental paths
-- converge on the same constraint (same auto name).
--
-- NOT VALID is required here, not optional (same as 0141/0142): post-team
-- migrations run as the database owner under FORCE RLS, and validating the
-- FK would scan bot_sessions through the team policy, whose
-- memoh_current_team_id() raises because the migration connection never
-- sets memoh.team_id. NOT VALID skips the pre-existing-rows scan; the
-- column is born NULL and every future write is still checked, so nothing
-- is lost. (0001 keeps the valid form — its team phase predates RLS.)
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

-- Seed query support: "the bot's (per user) most recent native session that
-- has a pair" (welcome composer seed). Existing
-- idx_bot_sessions_bot_mode_runtime_active_updated(bot_id, session_mode,
-- runtime_type, updated_at DESC) already narrows bot_id + mode/runtime +
-- recency; the created_by_user_id + IS NOT NULL pair filter applies on top
-- of a small set.
