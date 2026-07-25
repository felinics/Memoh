-- 0044_user_timezone
-- Add a timezone column to users for user-level timezone preferences.

ALTER TABLE users ADD COLUMN IF NOT EXISTS timezone TEXT NOT NULL DEFAULT 'UTC';
