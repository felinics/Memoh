-- 0095_channel_access_redesign (down)
-- Reverse 0095_channel_access_redesign in reverse creation order.

DROP INDEX IF EXISTS idx_channel_link_codes_user_id;
DROP TABLE IF EXISTS channel_link_codes;

DROP INDEX IF EXISTS idx_user_channel_identity_bindings_channel_identity_id;
DROP INDEX IF EXISTS idx_user_channel_identity_bindings_user_id;
DROP TABLE IF EXISTS user_channel_identity_bindings;

DROP INDEX IF EXISTS idx_bot_channel_admins_channel_identity_id;
DROP INDEX IF EXISTS idx_bot_channel_admins_bot_id;
DROP TABLE IF EXISTS bot_channel_admins;
