-- 0080_remove_channel_identity_binding (rollback)
-- Recreate channel identity user linking and one-time bind codes.

ALTER TABLE channel_identities
  ADD COLUMN IF NOT EXISTS user_id UUID REFERENCES users(id);

CREATE INDEX IF NOT EXISTS idx_channel_identities_user_id ON channel_identities(user_id);

CREATE TABLE IF NOT EXISTS channel_identity_bind_codes (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  token TEXT NOT NULL,
  issued_by_user_id UUID NOT NULL REFERENCES users(id),
  channel_type TEXT,
  expires_at TIMESTAMPTZ,
  used_at TIMESTAMPTZ,
  used_by_channel_identity_id UUID REFERENCES channel_identities(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT channel_identity_bind_codes_token_unique UNIQUE (token)
);

CREATE INDEX IF NOT EXISTS idx_channel_identity_bind_codes_channel_type ON channel_identity_bind_codes(channel_type);
