ALTER TABLE models ADD COLUMN IF NOT EXISTS is_multimodal BOOLEAN NOT NULL DEFAULT false;

UPDATE models SET is_multimodal = true WHERE 'image' = ANY(input_modalities);
UPDATE models SET is_multimodal = false WHERE NOT ('image' = ANY(input_modalities));

ALTER TABLE models DROP COLUMN IF EXISTS input_modalities;
ALTER TABLE models DROP COLUMN IF EXISTS output_modalities;
