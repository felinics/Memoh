-- storage_providers: pluggable object storage backends
CREATE TABLE IF NOT EXISTS storage_providers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  provider TEXT NOT NULL,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT storage_providers_name_unique UNIQUE (name),
  CONSTRAINT storage_providers_provider_check CHECK (provider IN ('localfs', 's3', 'gcs'))
);

-- bot_storage_bindings: per-bot storage backend selection
CREATE TABLE IF NOT EXISTS bot_storage_bindings (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  storage_provider_id UUID NOT NULL REFERENCES storage_providers(id) ON DELETE CASCADE,
  base_path TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT bot_storage_bindings_unique UNIQUE (bot_id)
);

CREATE INDEX IF NOT EXISTS idx_bot_storage_bindings_bot_id ON bot_storage_bindings(bot_id);

-- media_assets: immutable media objects with dedup by content hash
CREATE TABLE IF NOT EXISTS media_assets (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  storage_provider_id UUID REFERENCES storage_providers(id) ON DELETE SET NULL,
  content_hash TEXT NOT NULL,
  media_type TEXT NOT NULL,
  mime TEXT NOT NULL DEFAULT 'application/octet-stream',
  size_bytes BIGINT NOT NULL DEFAULT 0,
  storage_key TEXT NOT NULL,
  original_name TEXT,
  width INTEGER,
  height INTEGER,
  duration_ms BIGINT,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT media_assets_bot_hash_unique UNIQUE (bot_id, content_hash)
);

CREATE INDEX IF NOT EXISTS idx_media_assets_bot_id ON media_assets(bot_id);
CREATE INDEX IF NOT EXISTS idx_media_assets_content_hash ON media_assets(content_hash);

-- bot_history_message_assets: join table linking messages to media assets.
-- On fresh databases (0001 already defines this table with content_hash schema),
-- the CREATE is a no-op; the asset_id index only applies to old-schema upgrades.
CREATE TABLE IF NOT EXISTS bot_history_message_assets (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  message_id UUID NOT NULL REFERENCES bot_history_messages(id) ON DELETE CASCADE,
  asset_id UUID NOT NULL REFERENCES media_assets(id) ON DELETE CASCADE,
  role TEXT NOT NULL DEFAULT 'attachment',
  ordinal INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT message_asset_unique UNIQUE (message_id, asset_id)
);

CREATE INDEX IF NOT EXISTS idx_message_assets_message_id ON bot_history_message_assets(message_id);

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'bot_history_message_assets' AND column_name = 'asset_id'
  ) THEN
    CREATE INDEX IF NOT EXISTS idx_message_assets_asset_id ON bot_history_message_assets(asset_id);
  END IF;
END $$;
