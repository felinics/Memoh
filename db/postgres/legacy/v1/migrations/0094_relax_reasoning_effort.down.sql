-- 0094_relax_reasoning_effort (down)
-- Restore the previous fixed reasoning effort ladder. Rows with newer effort
-- tiers (minimal/max/custom) are reconciled into the old ladder first, otherwise
-- re-adding the CHECK constraint would fail on any row using a relaxed value.

-- Map relaxed tiers back into the old enum before re-adding the constraint:
--   minimal -> low, max -> xhigh, anything else out of range -> medium (default).
UPDATE bots SET reasoning_effort = CASE
  WHEN reasoning_effort = 'minimal' THEN 'low'
  WHEN reasoning_effort = 'max' THEN 'xhigh'
  ELSE 'medium'
END
WHERE reasoning_effort NOT IN ('none', 'low', 'medium', 'high', 'xhigh');

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'bots_reasoning_effort_check'
  ) THEN
    ALTER TABLE bots ADD CONSTRAINT bots_reasoning_effort_check
      CHECK (reasoning_effort IN ('none', 'low', 'medium', 'high', 'xhigh'));
  END IF;
END $$;
