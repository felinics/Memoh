-- 0053_add_image_model
-- Add image_model_id column to bots table for image generation model configuration
ALTER TABLE bots ADD COLUMN IF NOT EXISTS image_model_id UUID REFERENCES models(id) ON DELETE SET NULL;
