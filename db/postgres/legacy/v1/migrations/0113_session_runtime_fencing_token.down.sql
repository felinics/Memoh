-- 0113_session_runtime_fencing_token
-- Remove the distributed session runtime persistence fence.

ALTER TABLE user_input_requests
  DROP COLUMN IF EXISTS runtime_fencing_token;

ALTER TABLE tool_approval_requests
  DROP COLUMN IF EXISTS runtime_fencing_token;

ALTER TABLE bot_sessions
  DROP COLUMN IF EXISTS runtime_fencing_token;

DROP SEQUENCE IF EXISTS session_runtime_fencing_token_seq;
