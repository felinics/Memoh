-- 0018_inbox_refactor
-- Refactor bot_inbox: split content JSONB into content TEXT + header JSONB, add action column.

-- 1. Add new columns (idempotent).
ALTER TABLE bot_inbox ADD COLUMN IF NOT EXISTS header JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE bot_inbox ADD COLUMN IF NOT EXISTS action TEXT NOT NULL DEFAULT 'notify';

-- 2. Migrate data and convert column type.
--    Only needed when content is still JSONB (upgrade path).
--    On a fresh DB (0001_init already defines content as TEXT), this is a no-op.
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'bot_inbox' AND column_name = 'content' AND data_type = 'jsonb'
  ) THEN
    UPDATE bot_inbox
    SET header = content - 'text',
        action = 'notify'
    WHERE content IS NOT NULL AND content::text <> '{}';

    ALTER TABLE bot_inbox ALTER COLUMN content DROP DEFAULT;
    ALTER TABLE bot_inbox ALTER COLUMN content TYPE TEXT USING COALESCE(content ->> 'text', '');
    ALTER TABLE bot_inbox ALTER COLUMN content SET DEFAULT '';
  END IF;
END
$$;
