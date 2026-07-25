-- 0073_tool_approval (down)
-- Remove tool approval configuration and pending approval request storage.

DROP INDEX IF EXISTS idx_tool_approval_prompt_external;
DROP INDEX IF EXISTS idx_tool_approval_session_status_created;
DROP INDEX IF EXISTS idx_tool_approval_bot_status_created;

DROP TABLE IF EXISTS tool_approval_requests;

ALTER TABLE bots
  DROP COLUMN IF EXISTS tool_approval_config;
