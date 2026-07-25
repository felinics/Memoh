-- 0002_channel_identity_avatar
-- Add avatar_url column to channel_identities for sender profile display.
ALTER TABLE channel_identities ADD COLUMN IF NOT EXISTS avatar_url TEXT;
