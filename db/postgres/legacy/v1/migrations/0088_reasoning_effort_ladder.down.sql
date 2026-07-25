-- 0088_reasoning_effort_ladder
-- Restore the previous reasoning effort constraint.

UPDATE bots
SET reasoning_effort = 'medium'
WHERE reasoning_effort NOT IN ('low', 'medium', 'high');

ALTER TABLE bots DROP CONSTRAINT IF EXISTS bots_reasoning_effort_check;
ALTER TABLE bots ADD CONSTRAINT bots_reasoning_effort_check
  CHECK (reasoning_effort IN ('low', 'medium', 'high'));
