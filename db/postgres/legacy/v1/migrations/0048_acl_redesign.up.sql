-- 0044_acl_redesign
-- Redesign bot ACL rules to priority-based first-match-wins with new subject kinds.
-- Removes user_id subject support and guest_all fallback row in favor of bots.acl_default_effect.

-- 1. Add acl_default_effect to bots (default deny = closed-by-default, same as current behavior)
ALTER TABLE bots
  ADD COLUMN IF NOT EXISTS acl_default_effect TEXT NOT NULL DEFAULT 'deny';

ALTER TABLE bots
  DROP CONSTRAINT IF EXISTS bots_acl_default_effect_check;

ALTER TABLE bots
  ADD CONSTRAINT bots_acl_default_effect_check CHECK (acl_default_effect IN ('allow', 'deny'));

-- 2. Migrate existing guest_all allow rules -> set acl_default_effect = 'allow' on the bot
UPDATE bots
SET acl_default_effect = 'allow'
WHERE id IN (
  SELECT bot_id
  FROM bot_acl_rules
  WHERE action = 'chat.trigger'
    AND effect = 'allow'
    AND subject_kind = 'guest_all'
);

-- 3. Add new columns to bot_acl_rules
ALTER TABLE bot_acl_rules
  ADD COLUMN IF NOT EXISTS priority INTEGER NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS description TEXT,
  ADD COLUMN IF NOT EXISTS subject_channel_type TEXT;

-- 4. Assign priorities to existing channel_identity rules:
--    deny rules get priority 100, allow rules get priority 200
--    (preserving deny-before-allow behavior from the old evaluation pipeline)
UPDATE bot_acl_rules
SET priority = 100
WHERE subject_kind = 'channel_identity'
  AND effect = 'deny';

UPDATE bot_acl_rules
SET priority = 200
WHERE subject_kind = 'channel_identity'
  AND effect = 'allow';

-- 5. Delete all user-subject rules (no longer supported)
DELETE FROM bot_acl_rules WHERE subject_kind = 'user';

-- 6. Delete all guest_all rules (now represented by bots.acl_default_effect)
DELETE FROM bot_acl_rules WHERE subject_kind = 'guest_all';

-- 7. Drop old constraints before altering subject_kind values and columns
ALTER TABLE bot_acl_rules
  DROP CONSTRAINT IF EXISTS bot_acl_rules_subject_kind_check,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_subject_value_check,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_unique_user,
  DROP CONSTRAINT IF EXISTS bot_acl_rules_unique_channel_identity;

-- 8. Drop user_id column (no remaining user-subject rows)
ALTER TABLE bot_acl_rules
  DROP COLUMN IF EXISTS user_id;

DROP INDEX IF EXISTS idx_bot_acl_rules_user_id;

-- 9. Add updated constraints
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

-- 10. Add indexes for new query patterns
CREATE INDEX IF NOT EXISTS idx_bot_acl_rules_bot_priority ON bot_acl_rules(bot_id, priority ASC, created_at ASC)
  WHERE enabled = true;
