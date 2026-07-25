-- 0092_bot_workspace_resource_limits
-- Add desired per-bot workspace resource limits.
CREATE TABLE IF NOT EXISTS bot_workspace_resource_limits (
  bot_id UUID PRIMARY KEY REFERENCES bots(id) ON DELETE CASCADE,
  cpu_millicores BIGINT NOT NULL DEFAULT 0,
  memory_bytes BIGINT NOT NULL DEFAULT 0,
  storage_bytes BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT bot_workspace_resource_limits_cpu_check CHECK (cpu_millicores >= 0),
  CONSTRAINT bot_workspace_resource_limits_memory_check CHECK (memory_bytes >= 0),
  CONSTRAINT bot_workspace_resource_limits_storage_check CHECK (storage_bytes >= 0)
);
