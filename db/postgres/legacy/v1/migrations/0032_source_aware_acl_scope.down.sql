-- 0032_source_aware_acl_scope
-- Drop source-aware ACL scope fields after ensuring no scoped rules remain.

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM bot_acl_rules
    WHERE source_channel IS NOT NULL
       OR source_conversation_type IS NOT NULL
       OR source_conversation_id IS NOT NULL
       OR source_thread_id IS NOT NULL
  ) THEN
    RAISE EXCEPTION 'cannot rollback 0032_source_aware_acl_scope while scoped ACL rules exist';
  END IF;
END $$;

DROP INDEX IF EXISTS idx_bot_history_messages_identity_route_created;

ALTER TABLE bot_acl_rules
  DROP CONSTRAINT IF EXISTS bot_acl_rules_unique_user,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_unique_channel_identity,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_source_conversation_type_check,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_source_scope_check,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_source_thread_check;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'bot_acl_rules_unique_user'
  ) THEN
    ALTER TABLE bot_acl_rules
      ADD CONSTRAINT bot_acl_rules_unique_user UNIQUE NULLS NOT DISTINCT (
        bot_id, action, effect, subject_kind, user_id
      );
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'bot_acl_rules_unique_channel_identity'
  ) THEN
    ALTER TABLE bot_acl_rules
      ADD CONSTRAINT bot_acl_rules_unique_channel_identity UNIQUE NULLS NOT DISTINCT (
        bot_id, action, effect, subject_kind, channel_identity_id
      );
  END IF;
END $$;

ALTER TABLE bot_acl_rules
  DROP COLUMN IF EXISTS source_channel,
  DROP COLUMN IF EXISTS source_conversation_type,
  DROP COLUMN IF EXISTS source_conversation_id,
  DROP COLUMN IF EXISTS source_thread_id;
