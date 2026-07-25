-- 0102_acp_default_chat_runtime (down)
-- Remove bot default chat runtime fields and mode/runtime split columns.

DO $$
DECLARE
  has_acp_default_runtime BOOLEAN := false;
  has_unrepresentable_acp_sessions BOOLEAN := false;
BEGIN
  IF EXISTS (
    SELECT 1
    FROM information_schema.columns
    WHERE table_schema = current_schema()
      AND table_name = 'bots'
      AND column_name = 'chat_runtime'
  ) THEN
    EXECUTE 'SELECT EXISTS (SELECT 1 FROM bots WHERE chat_runtime = ''acp_agent'')'
      INTO has_acp_default_runtime;
  END IF;

  IF EXISTS (
    SELECT 1
    FROM information_schema.columns
    WHERE table_schema = current_schema()
      AND table_name = 'bot_sessions'
      AND column_name = 'runtime_type'
  ) THEN
    EXECUTE 'SELECT EXISTS (
      SELECT 1 FROM bot_sessions
      WHERE (runtime_type = ''acp_agent'') <> (type = ''acp_agent'')
    )'
      INTO has_unrepresentable_acp_sessions;
  END IF;

  IF has_acp_default_runtime OR has_unrepresentable_acp_sessions THEN
    RAISE EXCEPTION 'cannot downgrade acp default chat runtime while split-only ACP runtime data exists';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM information_schema.columns
    WHERE table_schema = current_schema()
      AND table_name = 'bot_sessions'
      AND column_name = 'runtime_metadata'
  ) THEN
    EXECUTE 'UPDATE bot_sessions
      SET metadata = COALESCE(metadata, ''{}''::jsonb) || COALESCE(runtime_metadata, ''{}''::jsonb)
      WHERE type = ''acp_agent'' AND runtime_type = ''acp_agent''';
  END IF;
END $$;

DROP INDEX IF EXISTS idx_bot_session_discuss_cursors_route;
DROP TABLE IF EXISTS bot_session_discuss_cursors;

DROP INDEX IF EXISTS idx_bot_sessions_bot_mode_runtime_active_updated;

ALTER TABLE bot_history_messages
  DROP COLUMN IF EXISTS runtime_type,
  DROP COLUMN IF EXISTS session_mode;

ALTER TABLE bot_sessions
  DROP COLUMN IF EXISTS runtime_metadata,
  DROP COLUMN IF EXISTS runtime_type,
  DROP COLUMN IF EXISTS session_mode;

ALTER TABLE bots
  DROP COLUMN IF EXISTS chat_acp_project_mode,
  DROP COLUMN IF EXISTS chat_acp_project_path,
  DROP COLUMN IF EXISTS chat_acp_agent_id,
  DROP COLUMN IF EXISTS chat_runtime;
