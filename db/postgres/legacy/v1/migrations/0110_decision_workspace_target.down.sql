-- 0110_decision_workspace_target (rollback)
-- Remove persisted workspace targets from deferred decision requests.

ALTER TABLE user_input_requests
  DROP COLUMN IF EXISTS workspace_target_id;

ALTER TABLE tool_approval_requests
  DROP COLUMN IF EXISTS workspace_target_id;
