-- 0111_user_input_text_interaction
-- Persist plain-text ask_user progress so channel replies survive restarts.

ALTER TABLE user_input_requests
  ADD COLUMN IF NOT EXISTS interaction_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN IF NOT EXISTS interaction_revision INTEGER NOT NULL DEFAULT 0;
