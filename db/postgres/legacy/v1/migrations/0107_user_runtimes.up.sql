-- 0107_user_runtimes
-- Add the API token registry for Remote Runtime WebSocket clients.

CREATE TABLE IF NOT EXISTS user_runtimes (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL CHECK (btrim(name) <> ''),
  api_token TEXT NOT NULL UNIQUE,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_runtimes_active_user_name
  ON user_runtimes(user_id, lower(name))
  WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_user_runtimes_user_id ON user_runtimes(user_id);

CREATE TABLE IF NOT EXISTS bot_remote_runtime_bindings (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  runtime_id UUID NOT NULL REFERENCES user_runtimes(id) ON DELETE RESTRICT,
  workspace_path TEXT NOT NULL CHECK (btrim(workspace_path) <> ''),
  is_primary BOOLEAN NOT NULL DEFAULT false,
  tool_approval_config JSONB NOT NULL DEFAULT '{"enabled":true,"read":{"mode":"allow","bypass_globs":[],"force_review_globs":[]},"write":{"mode":"ask","bypass_globs":[],"force_review_globs":[]},"exec":{"mode":"ask","bypass_commands":[],"force_review_commands":[]}}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (bot_id, runtime_id)
);

CREATE INDEX IF NOT EXISTS idx_bot_remote_runtime_bindings_runtime_id
  ON bot_remote_runtime_bindings(runtime_id);
CREATE INDEX IF NOT EXISTS idx_bot_remote_runtime_bindings_bot_id
  ON bot_remote_runtime_bindings(bot_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_remote_runtime_bindings_primary
  ON bot_remote_runtime_bindings(bot_id)
  WHERE is_primary = TRUE;
