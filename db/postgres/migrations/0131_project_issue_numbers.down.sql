-- 0131_project_issue_numbers (down)

DROP INDEX IF EXISTS public.project_nodes_issue_number_unique;

ALTER TABLE public.project_nodes
    DROP CONSTRAINT IF EXISTS project_nodes_number_check;

ALTER TABLE public.project_nodes
    DROP COLUMN IF EXISTS number;
