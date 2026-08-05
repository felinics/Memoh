-- 0128_drop_reasoning_enabled (down)
-- Restore the boolean flag from the effort value, then fold 'disable' back into
-- the pre-migration representation (flag off + a concrete tier), since the older
-- schema cannot store 'disable'.

-- Same RLS bracket as the up migration: these data steps run on a migration
-- connection that has no memoh.team_id.
ALTER TABLE bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bots DISABLE ROW LEVEL SECURITY;

ALTER TABLE bots ADD COLUMN IF NOT EXISTS reasoning_enabled BOOLEAN NOT NULL DEFAULT false;

UPDATE bots SET reasoning_enabled = (reasoning_effort <> 'disable');

UPDATE bots SET reasoning_effort = 'medium' WHERE reasoning_effort = 'disable';

ALTER TABLE bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE bots FORCE ROW LEVEL SECURITY;
