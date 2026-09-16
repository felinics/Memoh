-- 0154_add_reuse_tool_call_message_in_im (down)
-- NOTE: After rolling back this migration, re-run `sqlc generate` to update the
-- generated Go code in internal/db/postgres/sqlc/.

ALTER TABLE bots DROP COLUMN IF EXISTS reuse_tool_call_message_in_im;
