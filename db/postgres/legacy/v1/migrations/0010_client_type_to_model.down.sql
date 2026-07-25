-- 0010_client_type_to_model (down)
-- Reverse: restore client_type on llm_providers, remove from models.

-- 1) Re-add client_type to llm_providers (nullable first)
ALTER TABLE llm_providers ADD COLUMN IF NOT EXISTS client_type TEXT;

-- 2) Reverse-map from models back to providers (only chat models have client_type)
UPDATE llm_providers SET client_type = CASE m.client_type
    WHEN 'openai-responses' THEN 'openai'
    WHEN 'openai-completions' THEN 'openai-compat'
    WHEN 'anthropic-messages' THEN 'anthropic'
    WHEN 'google-generative-ai' THEN 'google'
END
FROM models m
WHERE m.llm_provider_id = llm_providers.id
  AND m.client_type IS NOT NULL;

-- 3) Default any remaining NULLs to 'openai'
UPDATE llm_providers SET client_type = 'openai' WHERE client_type IS NULL;

-- 4) Set NOT NULL + CHECK on providers
ALTER TABLE llm_providers ALTER COLUMN client_type SET NOT NULL;
ALTER TABLE llm_providers ADD CONSTRAINT llm_providers_client_type_check
  CHECK (client_type IN ('openai', 'openai-compat', 'anthropic', 'google', 'azure', 'bedrock', 'mistral', 'xai', 'ollama', 'dashscope'));

-- 5) Drop client_type from models
ALTER TABLE models DROP CONSTRAINT IF EXISTS models_chat_client_type_check;
ALTER TABLE models DROP CONSTRAINT IF EXISTS models_client_type_check;
ALTER TABLE models DROP COLUMN IF EXISTS client_type;
