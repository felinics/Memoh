-- Replace is_multimodal boolean with input modality array.
ALTER TABLE models ADD COLUMN IF NOT EXISTS input_modalities TEXT[] NOT NULL DEFAULT ARRAY['text']::TEXT[];

-- Migrate existing data (only when upgrading from old schema that had is_multimodal).
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'models' AND column_name = 'is_multimodal'
  ) THEN
    UPDATE models SET input_modalities = ARRAY['text','image']::TEXT[] WHERE is_multimodal = true;
    UPDATE models SET input_modalities = ARRAY['text']::TEXT[] WHERE is_multimodal = false;
  END IF;
END $$;

ALTER TABLE models DROP COLUMN IF EXISTS is_multimodal;
