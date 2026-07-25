-- 0079_acl_target_cleanup
-- Remove redundant ACL target kind and ordering fields.

ALTER TABLE bot_acl_rules
  DROP CONSTRAINT IF EXISTS bot_acl_rules_subject_kind_check,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_subject_value_check,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_unique_channel_identity,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_unique_target,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_target_check;

DROP INDEX IF EXISTS idx_bot_acl_rules_bot_priority;

ALTER TABLE bot_acl_rules
  DROP COLUMN IF EXISTS subject_kind,
  DROP COLUMN IF EXISTS priority;

ALTER TABLE bot_acl_rules
  ADD CONSTRAINT bot_acl_rules_target_check CHECK (
    subject_channel_type IS NULL OR btrim(subject_channel_type) <> ''
  ),
  ADD CONSTRAINT bot_acl_rules_unique_target UNIQUE NULLS NOT DISTINCT (
    bot_id, action, effect, channel_identity_id, subject_channel_type,
    source_conversation_type, source_conversation_id, source_thread_id
  );
