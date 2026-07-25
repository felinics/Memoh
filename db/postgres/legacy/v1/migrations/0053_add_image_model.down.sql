-- 0053_add_image_model (rollback)
-- Remove image_model_id column from bots table
ALTER TABLE bots DROP COLUMN IF EXISTS image_model_id;
