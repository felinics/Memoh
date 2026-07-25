-- 0086_heartbeat_default_interval (rollback)
-- Restore the previous default heartbeat interval.

ALTER TABLE bots ALTER COLUMN heartbeat_interval SET DEFAULT 30;
