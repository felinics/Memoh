-- 0156_remove_native_session_checkpoints
-- Restore the previous snapshot schema. Deleted snapshot data cannot be recovered.

ALTER TABLE public.agent_session_publications
    ADD COLUMN IF NOT EXISTS checkpoint_reset BOOLEAN NOT NULL DEFAULT false;
-- Existing publications have no snapshot to restore after downgrade. The
-- update spans every team; lift the request-scoped policies (memoh.team_id is
-- not set while migrating) and restore FORCE RLS after.
ALTER TABLE public.agent_session_publications NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.agent_session_publications DISABLE ROW LEVEL SECURITY;
UPDATE public.agent_session_publications SET checkpoint_reset = true;
ALTER TABLE public.agent_session_publications ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agent_session_publications FORCE ROW LEVEL SECURITY;

CREATE TABLE IF NOT EXISTS public.agent_session_states (
    team_id               UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                      REFERENCES public.teams(id) ON DELETE RESTRICT,
    session_id            UUID        NOT NULL,
    through_run_id        UUID        NOT NULL,
    agent_id              TEXT        NOT NULL,
    agent_session_id        TEXT        NOT NULL,
    cwd                   TEXT        NOT NULL,
    transcript_path       TEXT        NOT NULL,
    runtime_fencing_token BIGINT      NOT NULL,
    file_count            INTEGER     NOT NULL,
    record_count          BIGINT      NOT NULL,
    file_shapes           JSONB       NOT NULL DEFAULT '[]'::jsonb,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, session_id, through_run_id),
    CONSTRAINT agent_session_states_session_id_fkey
        FOREIGN KEY (team_id, session_id)
        REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE,
    CONSTRAINT agent_session_states_run_fkey
        FOREIGN KEY (team_id, session_id, through_run_id)
        REFERENCES public.session_runs(team_id, session_id, run_id) ON DELETE CASCADE,
    CONSTRAINT agent_session_states_agent_id_check
        CHECK (btrim(agent_id) <> '' AND octet_length(btrim(agent_id)) <= 256),
    CONSTRAINT agent_session_states_agent_session_id_check
        CHECK (btrim(agent_session_id) <> '' AND octet_length(btrim(agent_session_id)) <= 1024),
    CONSTRAINT agent_session_states_cwd_check
        CHECK (btrim(cwd) <> '' AND octet_length(btrim(cwd)) <= 16384),
    CONSTRAINT agent_session_states_transcript_path_check
        CHECK (
            transcript_path <> ''
            AND octet_length(transcript_path) <= 4096
            AND left(transcript_path, 1) <> '/'
            AND right(transcript_path, 6) = '.jsonl'
            AND position(chr(92) in transcript_path) = 0
            AND transcript_path !~ '(^|/)\.\.?(/|$)'
            AND transcript_path !~ E'[\r\n]'
        ),
    CONSTRAINT agent_session_states_runtime_fencing_token_check
        CHECK (runtime_fencing_token > 0),
    CONSTRAINT agent_session_states_file_count_check
        CHECK (file_count > 0 AND file_count <= 1024),
    CONSTRAINT agent_session_states_record_count_check
        CHECK (record_count > 0 AND record_count <= 2000000),
    CONSTRAINT agent_session_states_file_shapes_check
        CHECK (jsonb_typeof(file_shapes) = 'array')
);

-- Single line set per session: staging appends each file's tail after proving
-- the stored canonical prefix byte-identical. When the proof fails, staging
-- DECLINES without touching canonical rows - the turn publishes a reset head,
-- and only once that reset is canonical may the next turn stage a full
-- rewrite. Lines reference the session directly (not a version header)
-- because versions share them; version membership is defined by the header's
-- file_shapes.
CREATE TABLE IF NOT EXISTS public.agent_session_state_lines (
    team_id       UUID   NOT NULL DEFAULT public.memoh_current_team_id()
                          REFERENCES public.teams(id) ON DELETE RESTRICT,
    session_id    UUID   NOT NULL,
    file_path     TEXT COLLATE "C" NOT NULL,
    line_number   BIGINT NOT NULL,
    -- Verbatim compacted JSON text. TEXT (not JSONB) is deliberate: the
    -- capture digest, the append-only prefix proof, and the load-time digest
    -- verification all promise byte fidelity across the database round trip,
    -- which JSONB normalization (key order, whitespace, number rendering)
    -- would silently break. JSON validity is enforced by the adapter.
    content       TEXT   NOT NULL,
    content_bytes INTEGER NOT NULL,
    PRIMARY KEY (team_id, session_id, file_path, line_number),
    CONSTRAINT agent_session_state_lines_session_fkey
        FOREIGN KEY (team_id, session_id)
        REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE,
    CONSTRAINT agent_session_state_lines_file_path_check
        CHECK (
            file_path <> ''
            AND octet_length(file_path) <= 4096
            AND left(file_path, 1) <> '/'
            AND right(file_path, 6) = '.jsonl'
            AND position(chr(92) in file_path) = 0
            AND file_path !~ '(^|/)\.\.?(/|$)'
            AND file_path !~ E'[\r\n]'
        ),
    CONSTRAINT agent_session_state_lines_content_size_check
        CHECK (
            content_bytes = octet_length(content)
            AND content_bytes > 0
            AND content_bytes <= 8388608
        ),
    CONSTRAINT agent_session_state_lines_line_number_check
        CHECK (line_number > 0)
);

ALTER TABLE public.agent_session_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agent_session_states FORCE ROW LEVEL SECURITY;
ALTER TABLE public.agent_session_state_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agent_session_state_lines FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS agent_session_states_team_select ON public.agent_session_states;
CREATE POLICY agent_session_states_team_select ON public.agent_session_states
    FOR SELECT USING (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS agent_session_states_team_insert ON public.agent_session_states;
CREATE POLICY agent_session_states_team_insert ON public.agent_session_states
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS agent_session_states_team_update ON public.agent_session_states;
CREATE POLICY agent_session_states_team_update ON public.agent_session_states
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS agent_session_states_team_delete ON public.agent_session_states;
CREATE POLICY agent_session_states_team_delete ON public.agent_session_states
    FOR DELETE USING (team_id = public.memoh_current_team_id());

DROP POLICY IF EXISTS agent_session_state_lines_team_select ON public.agent_session_state_lines;
CREATE POLICY agent_session_state_lines_team_select ON public.agent_session_state_lines
    FOR SELECT USING (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS agent_session_state_lines_team_insert ON public.agent_session_state_lines;
CREATE POLICY agent_session_state_lines_team_insert ON public.agent_session_state_lines
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS agent_session_state_lines_team_update ON public.agent_session_state_lines;
CREATE POLICY agent_session_state_lines_team_update ON public.agent_session_state_lines
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
DROP POLICY IF EXISTS agent_session_state_lines_team_delete ON public.agent_session_state_lines;
CREATE POLICY agent_session_state_lines_team_delete ON public.agent_session_state_lines
    FOR DELETE USING (team_id = public.memoh_current_team_id());

