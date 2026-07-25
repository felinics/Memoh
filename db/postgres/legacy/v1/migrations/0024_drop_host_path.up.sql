-- 0024_drop_host_path
-- Remove host_path column from containers table (replaced by gRPC container access)
ALTER TABLE containers DROP COLUMN IF EXISTS host_path;
