-- 0032_source_aware_acl_scope
-- Add source-aware scope fields to bot ACL rules and index observed conversations.

ALTER TABLE bot_acl_rules
  ADD COLUMN IF NOT EXISTS source_channel TEXT,
  ADD COLUMN IF NOT EXISTS source_conversation_type TEXT,
  ADD COLUMN IF NOT EXISTS source_conversation_id TEXT,
  ADD COLUMN IF NOT EXISTS source_thread_id TEXT;

ALTER TABLE bot_acl_rules
  DROP CONSTRAINT IF EXISTS bot_acl_rules_unique_user,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_unique_channel_identity,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_source_conversation_type_check,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_source_scope_check,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_source_thread_check;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'bot_acl_rules_source_conversation_type_check'
  ) THEN
    ALTER TABLE bot_acl_rules
      ADD CONSTRAINT bot_acl_rules_source_conversation_type_check CHECK (
        source_conversation_type IS NULL OR source_conversation_type IN ('private', 'group', 'thread')
      );
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'bot_acl_rules_source_scope_check'
  ) THEN
    ALTER TABLE bot_acl_rules
      ADD CONSTRAINT bot_acl_rules_source_scope_check CHECK (
        (source_conversation_id IS NULL AND source_thread_id IS NULL)
        OR source_channel IS NOT NULL
      );
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'bot_acl_rules_source_thread_check'
  ) THEN
    ALTER TABLE bot_acl_rules
      ADD CONSTRAINT bot_acl_rules_source_thread_check CHECK (
        source_thread_id IS NULL OR source_conversation_id IS NOT NULL
      );
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'bot_acl_rules_unique_user'
  ) THEN
    ALTER TABLE bot_acl_rules
      ADD CONSTRAINT bot_acl_rules_unique_user UNIQUE NULLS NOT DISTINCT (
        bot_id, action, effect, subject_kind, user_id,
        source_channel, source_conversation_type, source_conversation_id, source_thread_id
      );
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'bot_acl_rules_unique_channel_identity'
  ) THEN
    ALTER TABLE bot_acl_rules
      ADD CONSTRAINT bot_acl_rules_unique_channel_identity UNIQUE NULLS NOT DISTINCT (
        bot_id, action, effect, subject_kind, channel_identity_id,
        source_channel, source_conversation_type, source_conversation_id, source_thread_id
      );
  END IF;
END $$;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'bot_history_messages' AND column_name = 'route_id'
  ) THEN
    CREATE INDEX IF NOT EXISTS idx_bot_history_messages_identity_route_created
      ON bot_history_messages(bot_id, sender_channel_identity_id, route_id, created_at DESC);
  END IF;
END $$;
