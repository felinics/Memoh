-- 0091_user_input_tool_call_unique
-- Make ask_user requests idempotent per session tool call.

WITH ranked AS (
  SELECT
    id,
    ROW_NUMBER() OVER (
      PARTITION BY session_id, tool_call_id
      ORDER BY
        CASE
          WHEN status IN ('submitted', 'canceled', 'failed', 'expired') THEN 0
          WHEN status = 'pending' THEN 1
          ELSE 2
        END,
        COALESCE(responded_at, canceled_at, updated_at, created_at) DESC,
        updated_at DESC,
        created_at DESC,
        id DESC
    ) AS rn
  FROM user_input_requests
)
DELETE FROM user_input_requests
WHERE id IN (SELECT id FROM ranked WHERE rn > 1);

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conrelid = 'user_input_requests'::regclass
      AND conname = 'user_input_tool_call_unique'
  ) THEN
    ALTER TABLE user_input_requests
      ADD CONSTRAINT user_input_tool_call_unique UNIQUE (session_id, tool_call_id);
  END IF;
END $$;
