-- 0083_add_bot_name
-- Add a globally-unique bot name (slug) used as the URL identifier, and harden
-- the bots table with NOT NULL, UNIQUE, and slug-format constraints.

ALTER TABLE bots ADD COLUMN IF NOT EXISTS name TEXT;

-- Backfill existing rows with a slug derived from display_name, deduplicated by
-- appending a numeric suffix on collision so the unique index can be created.
-- The candidate slug must fully satisfy bots_name_format_check
-- (^[a-z0-9][a-z0-9-]{1,62}$, i.e. 2-63 chars). Anything that does not match
-- (empty, single char, non-latin, etc.) falls back to 'bot' so the format
-- constraint added below can never be violated by backfilled data.
WITH candidate AS (
  SELECT
    id,
    trim(
      BOTH '-' FROM left(
        regexp_replace(
          lower(trim(COALESCE(display_name, ''))),
          '[^a-z0-9]+', '-', 'g'
        ),
        48
      )
    ) AS raw_slug
  FROM bots
  WHERE name IS NULL OR name = ''
),
base AS (
  SELECT
    id,
    CASE
      WHEN raw_slug ~ '^[a-z0-9][a-z0-9-]{1,62}$' THEN raw_slug
      ELSE 'bot'
    END AS slug
  FROM candidate
),
numbered AS (
  SELECT id, slug, row_number() OVER (PARTITION BY slug ORDER BY id) AS rn
  FROM base
)
UPDATE bots b
SET name = CASE WHEN n.rn = 1 THEN n.slug ELSE n.slug || '-' || n.rn END
FROM numbered n
WHERE b.id = n.id;

ALTER TABLE bots ALTER COLUMN name SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_bots_name ON bots(name);

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'bots_name_format_check'
  ) THEN
    ALTER TABLE bots
      ADD CONSTRAINT bots_name_format_check CHECK (name ~ '^[a-z0-9][a-z0-9-]{1,62}$');
  END IF;
END
$$;
