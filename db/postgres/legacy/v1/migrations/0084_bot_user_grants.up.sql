-- 0084_bot_user_grants
-- Add bot_user_grants table for workspace user access grants on a bot.
-- subject_type 'user' targets a specific workspace member; 'everyone' targets all members.
-- permissions is a JSON string array of grant scopes ('chat', 'manage').

CREATE TABLE IF NOT EXISTS bot_user_grants (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  subject_type TEXT NOT NULL,
  user_id UUID REFERENCES users(id) ON DELETE CASCADE,
  permissions JSONB NOT NULL DEFAULT '[]'::jsonb,
  created_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT bot_user_grants_subject_type_check CHECK (subject_type IN ('user', 'everyone')),
  CONSTRAINT bot_user_grants_subject_value_check CHECK (
    (subject_type = 'user' AND user_id IS NOT NULL) OR
    (subject_type = 'everyone' AND user_id IS NULL)
  )
);

CREATE INDEX IF NOT EXISTS idx_bot_user_grants_bot_id ON bot_user_grants(bot_id);
CREATE INDEX IF NOT EXISTS idx_bot_user_grants_user_id ON bot_user_grants(user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_user_grants_unique_user ON bot_user_grants(bot_id, user_id) WHERE subject_type = 'user';
CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_user_grants_unique_everyone ON bot_user_grants(bot_id) WHERE subject_type = 'everyone';
