-- 0065_add_context_token_budget (down)
-- NOTE: After rolling back this migration, re-run `sqlc generate` to update the
-- generated Go code in internal/db/postgres/sqlc/. The Go structs will still contain the
-- new columns until regenerated.

ALTER TABLE bots DROP COLUMN IF EXISTS persist_full_tool_results;
ALTER TABLE bots DROP COLUMN IF EXISTS context_token_budget;