-- 0136_remove_plugin_system
-- Retire the legacy Plugin installation model. Skill Packages and Connectors
-- are independent installation units and no longer carry Plugin ownership.

-- Migrations run without request-scoped team context. Temporarily disable RLS
-- so the destructive cleanup applies to every team, then restore it before
-- changing the table schema.
ALTER TABLE public.mcp_connections NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.mcp_connections DISABLE ROW LEVEL SECURITY;

-- Plugin-managed MCP connections are intentionally not migrated to user-owned
-- MCP connections or Connectors. Keeping them after removing the ownership
-- column would make stale Plugin resources indistinguishable from user data.
DELETE FROM public.mcp_connections
WHERE managed_by_plugin_installation_id IS NOT NULL;

ALTER TABLE public.mcp_connections ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.mcp_connections FORCE ROW LEVEL SECURITY;

DROP INDEX IF EXISTS public.idx_mcp_connections_plugin_installation_id;

ALTER TABLE public.mcp_connections
    DROP COLUMN IF EXISTS metadata,
    DROP COLUMN IF EXISTS visible,
    DROP COLUMN IF EXISTS managed_resource_key,
    DROP COLUMN IF EXISTS managed_by_plugin_installation_id;

DROP TABLE IF EXISTS public.bot_plugin_resources;
DROP TABLE IF EXISTS public.bot_plugin_installations;
