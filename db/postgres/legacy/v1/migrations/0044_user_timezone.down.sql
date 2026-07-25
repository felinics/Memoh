-- 0044_user_timezone
-- Remove the timezone column from users.

ALTER TABLE users DROP COLUMN IF EXISTS timezone;
