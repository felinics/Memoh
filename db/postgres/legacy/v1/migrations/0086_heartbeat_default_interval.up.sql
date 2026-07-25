-- 0086_heartbeat_default_interval
-- Change the default heartbeat interval to 24 hours without updating existing rows.

ALTER TABLE bots ALTER COLUMN heartbeat_interval SET DEFAULT 1440;
