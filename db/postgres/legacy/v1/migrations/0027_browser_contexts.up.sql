-- 0027_browser_contexts
-- Add browser_contexts table and browser_context_id to bots

CREATE TABLE IF NOT EXISTS browser_contexts (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name        TEXT NOT NULL DEFAULT '',
  config      JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE bots ADD COLUMN IF NOT EXISTS browser_context_id UUID REFERENCES browser_contexts(id) ON DELETE SET NULL;
