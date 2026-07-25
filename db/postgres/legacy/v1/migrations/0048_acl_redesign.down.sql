-- 0044_acl_redesign
-- Rollback: restore old bot_acl_rules schema and remove bots.acl_default_effect.

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM bot_acl_rules
    WHERE subject_kind IN ('all', 'channel_type')
  ) THEN
    RAISE EXCEPTION 'cannot rollback 0044_acl_redesign while "all" or "channel_type" ACL rules exist';
  END IF;
END $$;

-- Restore user_id column
ALTER TABLE bot_acl_rules
  ADD COLUMN IF NOT EXISTS user_id UUID REFERENCES users(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_bot_acl_rules_user_id ON bot_acl_rules(user_id);

-- Drop new columns
ALTER TABLE bot_acl_rules
  DROP COLUMN IF EXISTS priority,
  DROP COLUMN IF EXISTS enabled,
  DROP COLUMN IF EXISTS description,
  DROP COLUMN IF EXISTS subject_channel_type;

DROP INDEX IF EXISTS idx_bot_acl_rules_bot_priority;

-- Drop new constraints
ALTER TABLE bot_acl_rules
  DROP CONSTRAINT IF EXISTS bot_acl_rules_subject_kind_check,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_subject_value_check,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_unique_channel_identity;

-- Restore old constraints
ALTER TABLE bot_acl_rules
  ADD CONSTRAINT bot_acl_rules_subject_kind_check CHECK (subject_kind IN ('guest_all', 'user', 'channel_identity')),
  ADD CONSTRAINT bot_acl_rules_subject_value_check CHECK (
    (subject_kind = 'guest_all' AND user_id IS NULL AND channel_identity_id IS NULL) OR
    (subject_kind = 'user' AND user_id IS NOT NULL AND channel_identity_id IS NULL) OR
    (subject_kind = 'channel_identity' AND user_id IS NULL AND channel_identity_id IS NOT NULL)
  ),
  ADD CONSTRAINT bot_acl_rules_unique_user UNIQUE NULLS NOT DISTINCT (
    bot_id, action, effect, subject_kind, user_id,
    source_channel, source_conversation_type, source_conversation_id, source_thread_id
  ),
  ADD CONSTRAINT bot_acl_rules_unique_channel_identity UNIQUE NULLS NOT DISTINCT (
    bot_id, action, effect, subject_kind, channel_identity_id,
    source_channel, source_conversation_type, source_conversation_id, source_thread_id
  );

-- Remove acl_default_effect from bots
ALTER TABLE bots
  DROP CONSTRAINT IF EXISTS bots_acl_default_effect_check;

ALTER TABLE bots
  DROP COLUMN IF EXISTS acl_default_effect;
