-- 0031_chat_acl_remove_bot_members
-- Restore allow_guest, preauth, and bot_members, then drop bot ACL rules.

ALTER TABLE bots ADD COLUMN IF NOT EXISTS allow_guest BOOLEAN NOT NULL DEFAULT false;

UPDATE bots
SET allow_guest = true
WHERE type = 'public'
  AND EXISTS (
    SELECT 1
    FROM bot_acl_rules r
    WHERE r.bot_id = bots.id
      AND r.action = 'chat.trigger'
      AND r.effect = 'allow'
      AND r.subject_kind = 'guest_all'
  );

CREATE TABLE IF NOT EXISTS bot_preauth_keys (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  token TEXT NOT NULL,
  issued_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  expires_at TIMESTAMPTZ,
  used_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT bot_preauth_keys_unique UNIQUE (token)
);

CREATE INDEX IF NOT EXISTS idx_bot_preauth_keys_bot_id ON bot_preauth_keys(bot_id);
CREATE INDEX IF NOT EXISTS idx_bot_preauth_keys_expires ON bot_preauth_keys(expires_at);

CREATE TABLE IF NOT EXISTS bot_members (
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role TEXT NOT NULL DEFAULT 'member',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT bot_members_role_check CHECK (role IN ('owner', 'admin', 'member')),
  CONSTRAINT bot_members_unique UNIQUE (bot_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_bot_members_user_id ON bot_members(user_id);

DROP TABLE IF EXISTS bot_acl_rules;
