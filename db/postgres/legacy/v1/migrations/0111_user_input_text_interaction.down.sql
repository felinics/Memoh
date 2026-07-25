-- 0111_user_input_text_interaction
-- Remove persisted plain-text ask_user progress.

ALTER TABLE user_input_requests
  DROP COLUMN IF EXISTS interaction_revision,
  DROP COLUMN IF EXISTS interaction_json;
