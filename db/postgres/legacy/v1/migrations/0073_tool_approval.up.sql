-- 0073_tool_approval
-- Add tool approval configuration and pending approval request storage.

ALTER TABLE bots
  ADD COLUMN IF NOT EXISTS tool_approval_config JSONB NOT NULL DEFAULT '{"enabled":false,"write":{"require_approval":true,"bypass_globs":[".cache/**","tmp/**","node_modules/.cache/**","dist/**"]},"edit":{"require_approval":true,"bypass_globs":[".cache/**","tmp/**","node_modules/.cache/**","dist/**"]},"exec":{"require_approval":true,"bypass_commands":["npm","pnpm","yarn","bun","go","git"]}}'::jsonb;

CREATE TABLE IF NOT EXISTS tool_approval_requests (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  session_id UUID NOT NULL REFERENCES bot_sessions(id) ON DELETE CASCADE,
  route_id UUID REFERENCES bot_channel_routes(id) ON DELETE SET NULL,
  channel_identity_id UUID REFERENCES channel_identities(id) ON DELETE SET NULL,
  tool_call_id TEXT NOT NULL,
  tool_name TEXT NOT NULL,
  tool_input JSONB NOT NULL,
  short_id INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  decision_reason TEXT NOT NULL DEFAULT '',
  requested_by_channel_identity_id UUID REFERENCES channel_identities(id) ON DELETE SET NULL,
  decided_by_channel_identity_id UUID REFERENCES channel_identities(id) ON DELETE SET NULL,
  requested_message_id UUID REFERENCES bot_history_messages(id) ON DELETE SET NULL,
  prompt_message_id UUID REFERENCES bot_history_messages(id) ON DELETE SET NULL,
  prompt_external_message_id TEXT NOT NULL DEFAULT '',
  source_platform TEXT NOT NULL DEFAULT '',
  reply_target TEXT NOT NULL DEFAULT '',
  conversation_type TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_at TIMESTAMPTZ,
  CONSTRAINT tool_approval_tool_name_check CHECK (tool_name IN ('write', 'edit', 'exec')),
  CONSTRAINT tool_approval_status_check CHECK (status IN ('pending', 'approved', 'rejected', 'expired', 'cancelled')),
  CONSTRAINT tool_approval_short_id_unique UNIQUE (session_id, short_id),
  CONSTRAINT tool_approval_tool_call_unique UNIQUE (session_id, tool_call_id)
);

CREATE INDEX IF NOT EXISTS idx_tool_approval_bot_status_created
  ON tool_approval_requests(bot_id, status, created_at);
CREATE INDEX IF NOT EXISTS idx_tool_approval_session_status_created
  ON tool_approval_requests(session_id, status, created_at);
CREATE INDEX IF NOT EXISTS idx_tool_approval_prompt_external
  ON tool_approval_requests(prompt_external_message_id)
  WHERE prompt_external_message_id != '';
