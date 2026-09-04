-- 0147_context_fragment_texts
-- Content-addressed store of the rendered context fragments a run sent
-- (system prompt pieces, workspace rules, tool usage, skills, tool
-- definitions). One row per distinct fragment version per team, so the
-- trajectory can show the text that reached the model without copying the
-- prompt into every turn.

CREATE TABLE IF NOT EXISTS public.context_fragment_texts (
    team_id      UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                              REFERENCES public.teams(id) ON DELETE RESTRICT,
    content_hash TEXT        NOT NULL,
    kind         TEXT        NOT NULL,
    label        TEXT        NOT NULL DEFAULT '',
    text         TEXT        NOT NULL,
    text_bytes   INTEGER     NOT NULL,
    truncated    BOOLEAN     NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, content_hash)
);

ALTER TABLE public.context_fragment_texts ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.context_fragment_texts FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS context_fragment_texts_team_select ON public.context_fragment_texts;
CREATE POLICY context_fragment_texts_team_select ON public.context_fragment_texts
    FOR SELECT USING (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS context_fragment_texts_team_insert ON public.context_fragment_texts;
CREATE POLICY context_fragment_texts_team_insert ON public.context_fragment_texts
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS context_fragment_texts_team_update ON public.context_fragment_texts;
CREATE POLICY context_fragment_texts_team_update ON public.context_fragment_texts
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS context_fragment_texts_team_delete ON public.context_fragment_texts;
CREATE POLICY context_fragment_texts_team_delete ON public.context_fragment_texts
    FOR DELETE USING (team_id = public.memoh_current_team_id());
