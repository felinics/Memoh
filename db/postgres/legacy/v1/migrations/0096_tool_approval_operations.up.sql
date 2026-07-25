-- 0096_tool_approval_operations
-- Split approval operation from the original tool name and move policy to read/write/exec.

ALTER TABLE tool_approval_requests
  ADD COLUMN IF NOT EXISTS operation TEXT;

UPDATE tool_approval_requests
SET operation = CASE lower(trim(tool_name))
  WHEN 'read' THEN 'read'
  WHEN 'list' THEN 'read'
  WHEN 'exec' THEN 'exec'
  ELSE 'write'
END
WHERE operation IS NULL OR operation = '';

ALTER TABLE tool_approval_requests
  ALTER COLUMN operation SET NOT NULL;

ALTER TABLE tool_approval_requests
  DROP CONSTRAINT IF EXISTS tool_approval_tool_name_check;

DO $$
BEGIN
  ALTER TABLE tool_approval_requests
    ADD CONSTRAINT tool_approval_operation_check CHECK (operation IN ('read', 'write', 'exec'));
EXCEPTION
  WHEN duplicate_object THEN NULL;
END $$;

ALTER TABLE bots
  ALTER COLUMN tool_approval_config SET DEFAULT '{"enabled":false,"read":{"require_approval":false,"bypass_globs":[],"force_review_globs":[]},"write":{"require_approval":true,"bypass_globs":["/data/**","/tmp/**"],"force_review_globs":[]},"exec":{"require_approval":false,"bypass_commands":[],"force_review_commands":[]}}'::jsonb;

UPDATE bots
SET tool_approval_config = jsonb_build_object(
  'enabled', CASE
    WHEN jsonb_typeof(tool_approval_config->'enabled') = 'boolean'
      THEN tool_approval_config->'enabled'
    ELSE 'false'::jsonb
  END,
  'read', jsonb_build_object(
    'require_approval', CASE
      WHEN jsonb_typeof(tool_approval_config #> '{read,require_approval}') = 'boolean'
        THEN tool_approval_config #> '{read,require_approval}'
      ELSE 'false'::jsonb
    END,
    'bypass_globs', CASE
      WHEN jsonb_typeof(tool_approval_config #> '{read,bypass_globs}') = 'array'
        THEN tool_approval_config #> '{read,bypass_globs}'
      ELSE '[]'::jsonb
    END,
    'force_review_globs', CASE
      WHEN jsonb_typeof(tool_approval_config #> '{read,force_review_globs}') = 'array'
        THEN tool_approval_config #> '{read,force_review_globs}'
      ELSE '[]'::jsonb
    END
  ),
  'write', jsonb_build_object(
    'require_approval', to_jsonb(CASE
      WHEN tool_approval_config ? 'write' OR tool_approval_config ? 'edit' THEN
        CASE
          WHEN jsonb_typeof(tool_approval_config #> '{write,require_approval}') = 'boolean'
            THEN (tool_approval_config #>> '{write,require_approval}')::boolean
          ELSE false
        END OR
        CASE
          WHEN jsonb_typeof(tool_approval_config #> '{edit,require_approval}') = 'boolean'
            THEN (tool_approval_config #>> '{edit,require_approval}')::boolean
          ELSE false
        END
      ELSE true
    END),
    'bypass_globs', (
      SELECT COALESCE(jsonb_agg(DISTINCT to_jsonb(value)), '[]'::jsonb)
      FROM (
        SELECT jsonb_array_elements_text(CASE
          WHEN jsonb_typeof(tool_approval_config #> '{write,bypass_globs}') = 'array'
            THEN tool_approval_config #> '{write,bypass_globs}'
          ELSE '["/data/**","/tmp/**"]'::jsonb
        END) AS value
        UNION
        SELECT jsonb_array_elements_text(CASE
          WHEN jsonb_typeof(tool_approval_config #> '{edit,bypass_globs}') = 'array'
            THEN tool_approval_config #> '{edit,bypass_globs}'
          ELSE '[]'::jsonb
        END) AS value
      ) merged
    ),
    'force_review_globs', (
      SELECT COALESCE(jsonb_agg(DISTINCT to_jsonb(value)), '[]'::jsonb)
      FROM (
        SELECT jsonb_array_elements_text(CASE
          WHEN jsonb_typeof(tool_approval_config #> '{write,force_review_globs}') = 'array'
            THEN tool_approval_config #> '{write,force_review_globs}'
          ELSE '[]'::jsonb
        END) AS value
        UNION
        SELECT jsonb_array_elements_text(CASE
          WHEN jsonb_typeof(tool_approval_config #> '{edit,force_review_globs}') = 'array'
            THEN tool_approval_config #> '{edit,force_review_globs}'
          ELSE '[]'::jsonb
        END) AS value
      ) merged
    )
  ),
  'exec', jsonb_build_object(
    'require_approval', CASE
      WHEN jsonb_typeof(tool_approval_config #> '{exec,require_approval}') = 'boolean'
        THEN tool_approval_config #> '{exec,require_approval}'
      ELSE 'false'::jsonb
    END,
    'bypass_commands', CASE
      WHEN jsonb_typeof(tool_approval_config #> '{exec,bypass_commands}') = 'array'
        THEN tool_approval_config #> '{exec,bypass_commands}'
      ELSE '[]'::jsonb
    END,
    'force_review_commands', CASE
      WHEN jsonb_typeof(tool_approval_config #> '{exec,force_review_commands}') = 'array'
        THEN tool_approval_config #> '{exec,force_review_commands}'
      ELSE '[]'::jsonb
    END
  )
)
WHERE tool_approval_config IS NOT NULL;
