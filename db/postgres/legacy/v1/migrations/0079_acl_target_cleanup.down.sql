-- 0079_acl_target_cleanup
-- Restore ACL target kind and ordering fields.

ALTER TABLE bot_acl_rules
  DROP CONSTRAINT IF EXISTS bot_acl_rules_unique_target,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_target_check,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_subject_kind_check,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_subject_value_check,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_unique_channel_identity;

ALTER TABLE bot_acl_rules
  ADD COLUMN IF NOT EXISTS subject_kind TEXT,
  ADD COLUMN IF NOT EXISTS priority INTEGER NOT NULL DEFAULT 0;

UPDATE bot_acl_rules
SET subject_kind = CASE
  WHEN channel_identity_id IS NOT NULL THEN 'channel_identity'
  WHEN subject_channel_type IS NOT NULL THEN 'channel_type'
  ELSE 'all'
END
WHERE subject_kind IS NULL OR subject_kind = '';

UPDATE bot_acl_rules
SET subject_channel_type = NULL
WHERE channel_identity_id IS NOT NULL;

ALTER TABLE bot_acl_rules
  ALTER COLUMN subject_kind SET NOT NULL;

ALTER TABLE bot_acl_rules
  ADD CONSTRAINT bot_acl_rules_subject_kind_check CHECK (subject_kind IN ('all', 'channel_identity', 'channel_type')),
  ADD CONSTRAINT bot_acl_rules_subject_value_check CHECK (
    (subject_kind = 'all' AND channel_identity_id IS NULL AND subject_channel_type IS NULL) OR
    (subject_kind = 'channel_identity' AND channel_identity_id IS NOT NULL AND subject_channel_type IS NULL) OR
    (subject_kind = 'channel_type' AND channel_identity_id IS NULL AND subject_channel_type IS NOT NULL)
  ),
  ADD CONSTRAINT bot_acl_rules_unique_channel_identity UNIQUE NULLS NOT DISTINCT (
    bot_id, action, effect, subject_kind, channel_identity_id,
    source_conversation_type, source_conversation_id, source_thread_id
  );

CREATE INDEX IF NOT EXISTS idx_bot_acl_rules_bot_priority ON bot_acl_rules(bot_id, priority ASC, created_at ASC)
  WHERE enabled = true;
