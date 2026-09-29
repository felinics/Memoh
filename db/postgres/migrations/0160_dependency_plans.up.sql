-- 0160_dependency_plans
-- Freeze confirmed plans and protect dependency relationships in each native workspace.
CREATE TABLE IF NOT EXISTS bot_dependency_plans (
    team_id UUID NOT NULL DEFAULT public.memoh_current_team_id() REFERENCES teams(id) ON DELETE CASCADE,
    bot_id UUID NOT NULL,
    id TEXT NOT NULL,
    plan JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (team_id, bot_id) REFERENCES bots(team_id, id) ON DELETE CASCADE,
    PRIMARY KEY (team_id, bot_id, id)
);
CREATE TABLE IF NOT EXISTS bot_dependency_graphs (
    team_id UUID NOT NULL DEFAULT public.memoh_current_team_id() REFERENCES teams(id) ON DELETE CASCADE,
    bot_id UUID NOT NULL,
    owner TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ NOT NULL DEFAULT now(),
    graph JSONB NOT NULL DEFAULT '{}'::jsonb,
    FOREIGN KEY (team_id, bot_id) REFERENCES bots(team_id, id) ON DELETE CASCADE,
    PRIMARY KEY (team_id, bot_id)
);
ALTER TABLE bot_dependency_plans ENABLE ROW LEVEL SECURITY;
ALTER TABLE bot_dependency_plans FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS bot_dependency_plans_team ON bot_dependency_plans;
CREATE POLICY bot_dependency_plans_team ON bot_dependency_plans
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
ALTER TABLE bot_dependency_graphs ENABLE ROW LEVEL SECURITY;
ALTER TABLE bot_dependency_graphs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS bot_dependency_graphs_team ON bot_dependency_graphs;
CREATE POLICY bot_dependency_graphs_team ON bot_dependency_graphs
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
