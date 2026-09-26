-- 0157_agent_execution_budgets
-- Remove the per-schedule execution budget.
ALTER TABLE public.schedule DROP COLUMN IF EXISTS max_run_seconds;
