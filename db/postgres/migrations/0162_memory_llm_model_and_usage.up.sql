-- 0162_memory_llm_model_and_usage
-- Per-bot memory model (the LLM behind memory extract / decide / compact) and
-- a usage ledger for those calls. Memory LLM calls happen outside any chat
-- message, so their usage never reached bot_history_messages and the usage
-- page could not show it.
--
-- The column is memory_llm_model_id, not memory_model_id: 0020/0025 probe for
-- a legacy bots.memory_model_id and rewrite the memory provider when it
-- exists, so reusing that name would break a replay of those migrations.
--
-- FK form follows 0146: post-team composite (team_id, col) with the column
-- list on SET NULL, NOT VALID because post-team migrations run under FORCE RLS
-- without memoh.team_id. The column is born NULL, so nothing is skipped.

ALTER TABLE public.bots
  ADD COLUMN IF NOT EXISTS memory_llm_model_id UUID;

ALTER TABLE public.bots
  DROP CONSTRAINT IF EXISTS bots_memory_llm_model_id_fkey,
  ADD CONSTRAINT bots_memory_llm_model_id_fkey
    FOREIGN KEY (team_id, memory_llm_model_id)
    REFERENCES public.models(team_id, id)
    ON DELETE SET NULL (memory_llm_model_id)
    NOT VALID;

CREATE TABLE IF NOT EXISTS public.bot_memory_usage (
  id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  team_id    UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                         REFERENCES public.teams(id) ON DELETE RESTRICT,
  bot_id     UUID        NOT NULL,
  model_id   UUID,
  operation  TEXT        NOT NULL,
  usage      JSONB       NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT memoh_team_key_cdc0b6740345 UNIQUE (team_id, id),
  CONSTRAINT bot_memory_usage_operation_check
    CHECK (operation IN ('extract', 'decide', 'compact'))
);

ALTER TABLE public.bot_memory_usage
  DROP CONSTRAINT IF EXISTS bot_memory_usage_bot_id_fkey,
  ADD CONSTRAINT bot_memory_usage_bot_id_fkey
    FOREIGN KEY (team_id, bot_id)
    REFERENCES public.bots(team_id, id) ON DELETE CASCADE
    NOT VALID;

ALTER TABLE public.bot_memory_usage
  DROP CONSTRAINT IF EXISTS bot_memory_usage_model_id_fkey,
  ADD CONSTRAINT bot_memory_usage_model_id_fkey
    FOREIGN KEY (team_id, model_id)
    REFERENCES public.models(team_id, id)
    ON DELETE SET NULL (model_id)
    NOT VALID;

CREATE INDEX IF NOT EXISTS idx_bot_memory_usage_bot_created
  ON public.bot_memory_usage (team_id, bot_id, created_at DESC);

ALTER TABLE public.bot_memory_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_memory_usage FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS bot_memory_usage_team_select ON public.bot_memory_usage;
DROP POLICY IF EXISTS bot_memory_usage_team_insert ON public.bot_memory_usage;
DROP POLICY IF EXISTS bot_memory_usage_team_update ON public.bot_memory_usage;
DROP POLICY IF EXISTS bot_memory_usage_team_delete ON public.bot_memory_usage;

CREATE POLICY bot_memory_usage_team_select ON public.bot_memory_usage
  FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_memory_usage_team_insert ON public.bot_memory_usage
  FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_memory_usage_team_update ON public.bot_memory_usage
  FOR UPDATE
  USING (team_id = public.memoh_current_team_id())
  WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_memory_usage_team_delete ON public.bot_memory_usage
  FOR DELETE USING (team_id = public.memoh_current_team_id());
