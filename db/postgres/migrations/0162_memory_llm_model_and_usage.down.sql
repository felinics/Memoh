-- 0162_memory_llm_model_and_usage (down)
-- Dropping the table drops its policies, index and constraints; dropping the
-- column drops its FK.
DROP TABLE IF EXISTS public.bot_memory_usage;

ALTER TABLE public.bots
  DROP COLUMN IF EXISTS memory_llm_model_id;
