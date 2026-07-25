-- 0031_chat_acl_remove_bot_members
-- Add bot ACL rules, migrate allow_guest into ACL, and remove legacy bot sharing tables.

CREATE TABLE IF NOT EXISTS bot_acl_rules (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  action TEXT NOT NULL,
  effect TEXT NOT NULL,
  subject_kind TEXT NOT NULL,
  user_id UUID REFERENCES users(id) ON DELETE CASCADE,
  channel_identity_id UUID REFERENCES channel_identities(id) ON DELETE CASCADE,
  created_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT bot_acl_rules_action_check CHECK (action IN ('chat.trigger')),
  CONSTRAINT bot_acl_rules_effect_check CHECK (effect IN ('allow', 'deny')),
  CONSTRAINT bot_acl_rules_subject_kind_check CHECK (subject_kind IN ('guest_all', 'user', 'channel_identity')),
  CONSTRAINT bot_acl_rules_subject_value_check CHECK (
    (subject_kind = 'guest_all' AND user_id IS NULL AND channel_identity_id IS NULL) OR
    (subject_kind = 'user' AND user_id IS NOT NULL AND channel_identity_id IS NULL) OR
    (subject_kind = 'channel_identity' AND user_id IS NULL AND channel_identity_id IS NOT NULL)
  ),
  CONSTRAINT bot_acl_rules_unique_user UNIQUE NULLS NOT DISTINCT (bot_id, action, effect, subject_kind, user_id),
  CONSTRAINT bot_acl_rules_unique_channel_identity UNIQUE NULLS NOT DISTINCT (bot_id, action, effect, subject_kind, channel_identity_id)
);

CREATE INDEX IF NOT EXISTS idx_bot_acl_rules_bot_id ON bot_acl_rules(bot_id);
CREATE INDEX IF NOT EXISTS idx_bot_acl_rules_user_id ON bot_acl_rules(user_id);
CREATE INDEX IF NOT EXISTS idx_bot_acl_rules_channel_identity_id ON bot_acl_rules(channel_identity_id);

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM information_schema.columns c
    WHERE c.table_schema = 'public'
      AND c.table_name = 'bots'
      AND c.column_name = 'allow_guest'
  ) THEN
    EXECUTE $migrate$
      INSERT INTO bot_acl_rules (bot_id, action, effect, subject_kind, created_by_user_id)
      SELECT b.id, 'chat.trigger', 'allow', 'guest_all', b.owner_user_id
      FROM bots b
      WHERE b.type = 'public'
        AND b.allow_guest = true
      ON CONFLICT DO NOTHING
    $migrate$;
  END IF;
END $$;

ALTER TABLE bots DROP COLUMN IF EXISTS allow_guest;
DROP TABLE IF EXISTS bot_preauth_keys;
DROP TABLE IF EXISTS bot_members;
