-- 0020_memory_providers
-- Add memory_providers table, migrate bot memory/embedding model into provider config,
-- and drop the now-redundant columns from bots.

CREATE TABLE IF NOT EXISTS memory_providers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  provider TEXT NOT NULL,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  is_default BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT memory_providers_name_unique UNIQUE (name)
);

ALTER TABLE bots ADD COLUMN IF NOT EXISTS memory_provider_id UUID REFERENCES memory_providers(id) ON DELETE SET NULL;

-- Migrate: create a default builtin provider with existing model IDs, then link bots to it.
-- Guard: only reference old columns if they actually exist (fresh installs won't have them).
-- Uses dynamic SQL (EXECUTE) so PL/pgSQL doesn't validate column names at parse time.
DO $$
DECLARE
  _provider_id UUID;
  _has_old_cols BOOLEAN;
  _any_set BOOLEAN;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'bots' AND column_name = 'memory_model_id'
  ) INTO _has_old_cols;

  IF _has_old_cols THEN
    EXECUTE 'SELECT EXISTS (SELECT 1 FROM bots WHERE memory_model_id IS NOT NULL OR embedding_model_id IS NOT NULL)'
      INTO _any_set;

    IF _any_set THEN
      INSERT INTO memory_providers (name, provider, config, is_default)
      VALUES ('Built-in Memory', 'builtin', '{}'::jsonb, true)
      ON CONFLICT (name) DO UPDATE SET updated_at = now()
      RETURNING id INTO _provider_id;

      EXECUTE 'UPDATE bots SET memory_provider_id = $1 WHERE memory_model_id IS NOT NULL OR embedding_model_id IS NOT NULL'
        USING _provider_id;
    END IF;
  END IF;
END $$;

-- Drop the old columns (safe even if they don't exist).
ALTER TABLE bots DROP COLUMN IF EXISTS memory_model_id;
ALTER TABLE bots DROP COLUMN IF EXISTS embedding_model_id;
