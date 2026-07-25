-- 0113_session_runtime_fencing_token
-- Add a monotonic PostgreSQL fence for distributed session runtime writes.

CREATE SEQUENCE IF NOT EXISTS session_runtime_fencing_token_seq AS BIGINT NO CYCLE;

ALTER TABLE bot_sessions
  ADD COLUMN IF NOT EXISTS runtime_fencing_token BIGINT NOT NULL DEFAULT 0
  CHECK (runtime_fencing_token >= 0);

ALTER TABLE tool_approval_requests
  ADD COLUMN IF NOT EXISTS runtime_fencing_token BIGINT;

ALTER TABLE user_input_requests
  ADD COLUMN IF NOT EXISTS runtime_fencing_token BIGINT;
