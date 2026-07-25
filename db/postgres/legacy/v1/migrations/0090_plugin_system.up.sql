-- 0090_plugin_system
-- Add plugin installation records and plugin-managed MCP metadata.

CREATE TABLE IF NOT EXISTS bot_plugin_installations (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  plugin_id TEXT NOT NULL,
  plugin_name TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'ready',
  enabled BOOLEAN NOT NULL DEFAULT true,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  manifest JSONB NOT NULL DEFAULT '{}'::jsonb,
  installed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT bot_plugin_installations_unique UNIQUE (bot_id, plugin_id)
);

CREATE INDEX IF NOT EXISTS idx_bot_plugin_installations_bot_id ON bot_plugin_installations(bot_id);
CREATE INDEX IF NOT EXISTS idx_bot_plugin_installations_plugin_id ON bot_plugin_installations(plugin_id);

ALTER TABLE mcp_connections
  ADD COLUMN IF NOT EXISTS managed_by_plugin_installation_id UUID REFERENCES bot_plugin_installations(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS managed_resource_key TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS visible BOOLEAN NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE INDEX IF NOT EXISTS idx_mcp_connections_plugin_installation_id ON mcp_connections(managed_by_plugin_installation_id);

CREATE TABLE IF NOT EXISTS bot_plugin_resources (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  installation_id UUID NOT NULL REFERENCES bot_plugin_installations(id) ON DELETE CASCADE,
  resource_type TEXT NOT NULL,
  resource_key TEXT NOT NULL,
  resource_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT bot_plugin_resources_unique UNIQUE (installation_id, resource_type, resource_key)
);

CREATE INDEX IF NOT EXISTS idx_bot_plugin_resources_installation_id ON bot_plugin_resources(installation_id);
CREATE INDEX IF NOT EXISTS idx_bot_plugin_resources_resource ON bot_plugin_resources(resource_type, resource_id);
