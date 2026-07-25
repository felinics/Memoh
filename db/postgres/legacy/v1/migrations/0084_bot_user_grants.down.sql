-- 0084_bot_user_grants
-- Reverse the bot_user_grants table.

DROP INDEX IF EXISTS idx_bot_user_grants_unique_everyone;
DROP INDEX IF EXISTS idx_bot_user_grants_unique_user;
DROP INDEX IF EXISTS idx_bot_user_grants_user_id;
DROP INDEX IF EXISTS idx_bot_user_grants_bot_id;
DROP TABLE IF EXISTS bot_user_grants;
