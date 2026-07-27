-- 0122_decision_response_idempotency
-- Remove client response identities from the decision rows.

ALTER TABLE public.user_input_requests
    DROP CONSTRAINT IF EXISTS user_input_response_identity_check,
    DROP COLUMN IF EXISTS response_payload_hash,
    DROP COLUMN IF EXISTS response_control_id;

ALTER TABLE public.tool_approval_requests
    DROP CONSTRAINT IF EXISTS tool_approval_response_identity_check,
    DROP COLUMN IF EXISTS response_payload_hash,
    DROP COLUMN IF EXISTS response_control_id;
