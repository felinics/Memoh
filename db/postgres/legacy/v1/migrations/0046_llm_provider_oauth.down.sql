-- 0046_llm_provider_oauth (rollback)
-- Remove OAuth token storage for LLM providers.

DROP INDEX IF EXISTS idx_llm_provider_oauth_tokens_state;
DROP TABLE IF EXISTS llm_provider_oauth_tokens;
