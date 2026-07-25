-- 0080_remove_channel_identity_binding
-- Remove one-time bind codes and user linking from channel identities.

DROP TABLE IF EXISTS channel_identity_bind_codes;
DROP INDEX IF EXISTS idx_channel_identities_user_id;
ALTER TABLE channel_identities DROP COLUMN IF EXISTS user_id;
