-- 0096_tool_approval_operations (down)
-- Restore the previous tool-name constrained approval request shape.

ALTER TABLE tool_approval_requests
  DROP CONSTRAINT IF EXISTS tool_approval_operation_check;

UPDATE tool_approval_requests
SET tool_name = CASE
  WHEN tool_name IN ('write', 'edit', 'exec') THEN tool_name
  WHEN operation = 'exec' THEN 'exec'
  ELSE 'write'
END;

ALTER TABLE tool_approval_requests
  DROP COLUMN IF EXISTS operation;

DO $$
BEGIN
  ALTER TABLE tool_approval_requests
    ADD CONSTRAINT tool_approval_tool_name_check CHECK (tool_name IN ('write', 'edit', 'exec'));
EXCEPTION
  WHEN duplicate_object THEN NULL;
END $$;

ALTER TABLE bots
  ALTER COLUMN tool_approval_config SET DEFAULT '{"enabled":false,"write":{"require_approval":true,"bypass_globs":["/data/**","/tmp/**"],"force_review_globs":[]},"edit":{"require_approval":true,"bypass_globs":["/data/**","/tmp/**"],"force_review_globs":[]},"exec":{"require_approval":false,"bypass_commands":[],"force_review_commands":[]}}'::jsonb;

UPDATE bots
SET tool_approval_config = jsonb_build_object(
  'enabled', COALESCE(tool_approval_config->'enabled', 'false'::jsonb),
  'write', COALESCE(tool_approval_config->'write', '{"require_approval":true,"bypass_globs":["/data/**","/tmp/**"],"force_review_globs":[]}'::jsonb),
  'edit', COALESCE(tool_approval_config->'write', '{"require_approval":true,"bypass_globs":["/data/**","/tmp/**"],"force_review_globs":[]}'::jsonb),
  'exec', COALESCE(tool_approval_config->'exec', '{"require_approval":false,"bypass_commands":[],"force_review_commands":[]}'::jsonb)
)
WHERE tool_approval_config IS NOT NULL;
