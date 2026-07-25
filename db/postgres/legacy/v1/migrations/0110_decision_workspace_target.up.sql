-- 0110_decision_workspace_target
-- Preserve the selected workspace target across deferred approval and user input flows.

ALTER TABLE tool_approval_requests
  ADD COLUMN IF NOT EXISTS workspace_target_id TEXT NOT NULL DEFAULT '';

ALTER TABLE user_input_requests
  ADD COLUMN IF NOT EXISTS workspace_target_id TEXT NOT NULL DEFAULT '';
