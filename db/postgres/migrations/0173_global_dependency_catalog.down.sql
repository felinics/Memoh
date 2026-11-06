-- 0173_global_dependency_catalog
-- Drop the global dependency catalog tables. The per-team public tables were
-- left in place by the up migration and still hold their original rows.

DROP TABLE IF EXISTS template.workspace_dependency_catalogs;
DROP TABLE IF EXISTS template.workspace_dependency_definitions;
