-- 0072_add_show_tool_calls_in_im (down)
-- NOTE: After rolling back this migration, re-run `sqlc generate` to update the
-- generated Go code in internal/db/postgres/sqlc/.

ALTER TABLE bots DROP COLUMN IF EXISTS show_tool_calls_in_im;
