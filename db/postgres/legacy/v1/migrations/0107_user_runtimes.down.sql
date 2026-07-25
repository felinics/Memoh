-- 0107_user_runtimes (rollback)
-- Remove the Remote Runtime credential registry.

DROP TABLE IF EXISTS bot_remote_runtime_bindings;
DROP TABLE IF EXISTS user_runtimes;
