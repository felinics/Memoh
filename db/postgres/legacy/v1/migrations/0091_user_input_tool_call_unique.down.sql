-- 0091_user_input_tool_call_unique
-- Remove ask_user per-tool-call idempotency.

ALTER TABLE user_input_requests
  DROP CONSTRAINT IF EXISTS user_input_tool_call_unique;
