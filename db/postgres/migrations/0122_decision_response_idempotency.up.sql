-- 0122_decision_response_idempotency
-- Keep the client response identity on the authoritative decision row so a
-- terminal response remains replayable without a second persistence model.

ALTER TABLE public.tool_approval_requests
    ADD COLUMN IF NOT EXISTS response_control_id TEXT,
    ADD COLUMN IF NOT EXISTS response_payload_hash TEXT,
    DROP CONSTRAINT IF EXISTS tool_approval_response_identity_check,
    ADD CONSTRAINT tool_approval_response_identity_check CHECK (
        (response_control_id IS NULL) = (response_payload_hash IS NULL)
    );

ALTER TABLE public.user_input_requests
    ADD COLUMN IF NOT EXISTS response_control_id TEXT,
    ADD COLUMN IF NOT EXISTS response_payload_hash TEXT,
    DROP CONSTRAINT IF EXISTS user_input_response_identity_check,
    ADD CONSTRAINT user_input_response_identity_check CHECK (
        (response_control_id IS NULL) = (response_payload_hash IS NULL)
    );
