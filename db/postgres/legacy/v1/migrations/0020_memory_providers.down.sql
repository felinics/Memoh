-- 0020_memory_providers (rollback)

ALTER TABLE bots ADD COLUMN IF NOT EXISTS memory_model_id UUID REFERENCES models(id) ON DELETE SET NULL;
ALTER TABLE bots ADD COLUMN IF NOT EXISTS embedding_model_id UUID REFERENCES models(id) ON DELETE SET NULL;
ALTER TABLE bots DROP COLUMN IF EXISTS memory_provider_id;
DROP TABLE IF EXISTS memory_providers;
