-- 0018_inbox_refactor (down)
-- Revert bot_inbox to original schema: merge header+content back into content JSONB.

-- 1. Convert content back to JSONB, merging header and text.
ALTER TABLE bot_inbox ALTER COLUMN content DROP DEFAULT;
ALTER TABLE bot_inbox ALTER COLUMN content TYPE JSONB USING (COALESCE(header, '{}'::jsonb) || jsonb_build_object('text', content));
ALTER TABLE bot_inbox ALTER COLUMN content SET DEFAULT '{}'::jsonb;

-- 2. Drop added columns.
ALTER TABLE bot_inbox DROP COLUMN IF EXISTS action;
ALTER TABLE bot_inbox DROP COLUMN IF EXISTS header;
