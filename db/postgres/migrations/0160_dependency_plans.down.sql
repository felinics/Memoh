-- 0160_dependency_plans
-- Remove dependency plan storage without touching installed workspace files.
DROP TABLE IF EXISTS bot_dependency_graphs;
DROP TABLE IF EXISTS bot_dependency_plans;
