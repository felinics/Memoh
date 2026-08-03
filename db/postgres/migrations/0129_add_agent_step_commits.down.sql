-- 0129_add_agent_step_commits (down)
-- Remove the complete native-agent step idempotency ledger.

DROP TABLE IF EXISTS public.agent_step_commits;
