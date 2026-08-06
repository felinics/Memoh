-- 0130_projects (down)
-- Drops the Project collaboration tables in reverse dependency order.

DROP TABLE IF EXISTS public.project_node_labels;
DROP TABLE IF EXISTS public.project_labels;
DROP TABLE IF EXISTS public.project_node_links;
DROP TABLE IF EXISTS public.project_comments;
DROP TABLE IF EXISTS public.project_issue_activity;
DROP TABLE IF EXISTS public.project_issue_details;
DROP TABLE IF EXISTS public.project_node_versions;
DROP TABLE IF EXISTS public.project_nodes;
DROP TABLE IF EXISTS public.projects;

-- pg_trgm was introduced by this migration; nothing else in the schema
-- depends on it.
DROP EXTENSION IF EXISTS pg_trgm;
