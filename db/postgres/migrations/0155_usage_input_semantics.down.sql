-- 0155_usage_input_semantics
-- Remove the legacy input usage compatibility function.

DROP FUNCTION IF EXISTS public.memoh_usage_input_tokens(jsonb, jsonb, text);
