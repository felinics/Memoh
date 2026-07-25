-- 0102_acp_default_chat_runtime
-- Add bot default chat runtime fields and split session mode from runtime type.

ALTER TABLE bots
  ADD COLUMN IF NOT EXISTS chat_runtime TEXT NOT NULL DEFAULT 'model' CHECK (chat_runtime IN ('model', 'acp_agent')),
  ADD COLUMN IF NOT EXISTS chat_acp_agent_id TEXT,
  ADD COLUMN IF NOT EXISTS chat_acp_project_path TEXT NOT NULL DEFAULT '/data',
  ADD COLUMN IF NOT EXISTS chat_acp_project_mode TEXT NOT NULL DEFAULT 'project' CHECK (chat_acp_project_mode IN ('project', 'none'));

ALTER TABLE bot_sessions
  ADD COLUMN IF NOT EXISTS session_mode TEXT NOT NULL DEFAULT 'chat' CHECK (session_mode IN ('chat', 'discuss', 'heartbeat', 'schedule', 'subagent')),
  ADD COLUMN IF NOT EXISTS runtime_type TEXT NOT NULL DEFAULT 'model' CHECK (runtime_type IN ('model', 'acp_agent')),
  ADD COLUMN IF NOT EXISTS runtime_metadata JSONB NOT NULL DEFAULT '{}'::jsonb;

UPDATE bot_sessions
SET session_mode = CASE
      WHEN type = 'acp_agent' THEN 'chat'
      ELSE type
    END,
    runtime_type = CASE
      WHEN type = 'acp_agent' THEN 'acp_agent'
      ELSE 'model'
    END,
    runtime_metadata = CASE
      WHEN type = 'acp_agent' THEN jsonb_strip_nulls(jsonb_build_object(
        'acp_agent_id', metadata->>'acp_agent_id',
        'project_path', COALESCE(NULLIF(metadata->>'project_path', ''), '/data'),
        'acp_project_mode', COALESCE(NULLIF(metadata->>'acp_project_mode', ''), 'project'),
        'runtime_owner_account_id', COALESCE(NULLIF(metadata->>'runtime_owner_account_id', ''), created_by_user_id::text)
      ))
      ELSE '{}'::jsonb
    END
WHERE session_mode = 'chat'
  AND runtime_type = 'model'
  AND runtime_metadata = '{}'::jsonb;

CREATE INDEX IF NOT EXISTS idx_bot_sessions_bot_mode_runtime_active_updated
  ON bot_sessions(bot_id, session_mode, runtime_type, updated_at DESC, id DESC)
  WHERE deleted_at IS NULL;

ALTER TABLE bot_history_messages
  ADD COLUMN IF NOT EXISTS session_mode TEXT NOT NULL DEFAULT 'chat' CHECK (session_mode IN ('chat', 'discuss', 'heartbeat', 'schedule', 'subagent')),
  ADD COLUMN IF NOT EXISTS runtime_type TEXT NOT NULL DEFAULT 'model' CHECK (runtime_type IN ('model', 'acp_agent'));

UPDATE bot_history_messages AS m
SET session_mode = COALESCE(
      CASE WHEN s.type = 'subagent' THEN ps.session_mode ELSE s.session_mode END,
      'chat'
    ),
    runtime_type = COALESCE(
      CASE WHEN s.type = 'subagent' THEN ps.runtime_type ELSE s.runtime_type END,
      'model'
    )
FROM bot_sessions AS s
LEFT JOIN bot_sessions AS ps ON ps.id = s.parent_session_id
WHERE m.session_id = s.id;

CREATE TABLE IF NOT EXISTS bot_session_discuss_cursors (
  session_id UUID NOT NULL REFERENCES bot_sessions(id) ON DELETE CASCADE,
  scope_key TEXT NOT NULL DEFAULT 'default',
  route_id UUID REFERENCES bot_channel_routes(id) ON DELETE SET NULL,
  source TEXT NOT NULL DEFAULT '',
  consumed_cursor BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (session_id, scope_key)
);

CREATE INDEX IF NOT EXISTS idx_bot_session_discuss_cursors_route
  ON bot_session_discuss_cursors(route_id)
  WHERE route_id IS NOT NULL;
