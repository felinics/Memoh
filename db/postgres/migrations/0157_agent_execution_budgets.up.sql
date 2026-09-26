-- 0157_agent_execution_budgets
-- Add the per-schedule execution budget without changing runtime ownership.
ALTER TABLE public.schedule ADD COLUMN IF NOT EXISTS max_run_seconds integer NOT NULL DEFAULT 3600 CHECK (max_run_seconds BETWEEN 300 AND 86400);
