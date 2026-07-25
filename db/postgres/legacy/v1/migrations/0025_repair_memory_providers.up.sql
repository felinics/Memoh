-- 0025_repair_memory_providers
-- Repair migration: re-apply 0020_memory_providers for databases where it was
-- skipped due to migration renumbering (0020 was originally add_model_id_tracking,
-- later renumbered to 0021 when memory_providers was inserted as 0020).
-- All statements are idempotent, so this is safe for databases where 0020 already applied.

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

ALTER TABLE bots DROP COLUMN IF EXISTS memory_model_id;
ALTER TABLE bots DROP COLUMN IF EXISTS embedding_model_id;
