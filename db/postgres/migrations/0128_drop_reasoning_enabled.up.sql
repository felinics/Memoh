-- 0128_drop_reasoning_enabled
-- Collapse the reasoning on/off flag into reasoning_effort. The effort column
-- becomes the single source of truth: 'disable' means no reasoning, any other
-- value is a concrete tier, and the column default ('medium') is what a bot that
-- was never configured resolves to.
--
-- Existing rows map reasoning_enabled = false to 'disable', so upgrading never
-- silently turns reasoning on (and its cost) for a running deployment. Because
-- the column default was false, this covers nearly every existing bot; raising
-- those to a real tier is deliberately left to a separate, opt-in migration.

-- bots is under FORCE row level security keyed on memoh.team_id, which a
-- migration connection never sets, so a bare UPDATE fails with "memoh.team_id is
-- not set". Lift the policy for the data step and restore it after, the same way
-- 0125 does.
ALTER TABLE bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bots DISABLE ROW LEVEL SECURITY;

-- Guarded so a re-run after the column is already dropped is a no-op rather than
-- an "column reasoning_enabled does not exist" error.
DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM pg_attribute
    WHERE attrelid = 'bots'::regclass
      AND attname = 'reasoning_enabled'
      AND NOT attisdropped
  ) THEN
    UPDATE bots SET reasoning_effort = 'disable' WHERE reasoning_enabled = false;
  END IF;
END
$$;

ALTER TABLE bots DROP COLUMN IF EXISTS reasoning_enabled;

ALTER TABLE bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE bots FORCE ROW LEVEL SECURITY;
