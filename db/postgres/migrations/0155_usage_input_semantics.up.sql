-- 0155_usage_input_semantics
-- Normalize legacy Anthropic input totals without rewriting usage records.

CREATE OR REPLACE FUNCTION public.memoh_usage_input_tokens(token_usage jsonb, message_metadata jsonb, message_runtime text)
RETURNS bigint
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $$
  SELECT COALESCE((token_usage->>'inputTokens')::bigint, 0)
    + CASE
        WHEN message_runtime = 'model'
          AND message_metadata->'context_lifecycle'->>'client_type' = 'anthropic-messages'
          AND NOT token_usage ? 'inputTokenSemantics'
        THEN COALESCE((token_usage->'inputTokenDetails'->>'cacheReadTokens')::bigint, 0)
           + COALESCE((token_usage->'inputTokenDetails'->>'cacheWriteTokens')::bigint, 0)
        ELSE 0
      END
$$;
