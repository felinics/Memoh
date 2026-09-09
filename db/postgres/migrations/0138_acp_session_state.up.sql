-- 0138_acp_session_state
-- Persist ordered ACP JSONL session state independently of ephemeral runtime
-- homes. Lines form a single per-session set that staging appends to; version
-- headers are small run-keyed rows carrying each file's shape (record count +
-- content digest). A staged version is resume-eligible only after its run
-- becomes the session's canonical ACP publication head, and readers bound
-- every file by the head version's shape so a dangling candidate tail from a
-- crashed round is invisible.

-- through_run_id must belong to the same tenant and session as the snapshot.
-- 0137 builds the redundant candidate key's unique index concurrently. Attach
-- it as a constraint here so PostgreSQL can enforce the complete invariant
-- without rebuilding the index while session_runs is write-locked.
DO $add_session_runs_team_session_run_key$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.session_runs'::regclass
          AND conname = 'session_runs_team_session_run_key'
    ) THEN
        ALTER TABLE public.session_runs
            ADD CONSTRAINT session_runs_team_session_run_key
            UNIQUE USING INDEX session_runs_team_session_run_key;
    END IF;
END
$add_session_runs_team_session_run_key$;

CREATE TABLE IF NOT EXISTS public.acp_session_states (
    team_id               UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                      REFERENCES public.teams(id) ON DELETE RESTRICT,
    session_id            UUID        NOT NULL,
    through_run_id        UUID        NOT NULL,
    agent_id              TEXT        NOT NULL,
    acp_session_id        TEXT        NOT NULL,
    cwd                   TEXT        NOT NULL,
    transcript_path       TEXT        NOT NULL,
    runtime_fencing_token BIGINT      NOT NULL,
    file_count            INTEGER     NOT NULL,
    record_count          BIGINT      NOT NULL,
    file_shapes           JSONB       NOT NULL DEFAULT '[]'::jsonb,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, session_id, through_run_id),
    CONSTRAINT acp_session_states_session_id_fkey
        FOREIGN KEY (team_id, session_id)
        REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE,
    CONSTRAINT acp_session_states_run_fkey
        FOREIGN KEY (team_id, session_id, through_run_id)
        REFERENCES public.session_runs(team_id, session_id, run_id) ON DELETE CASCADE,
    CONSTRAINT acp_session_states_agent_id_check
        CHECK (btrim(agent_id) <> '' AND octet_length(btrim(agent_id)) <= 256),
    CONSTRAINT acp_session_states_acp_session_id_check
        CHECK (btrim(acp_session_id) <> '' AND octet_length(btrim(acp_session_id)) <= 1024),
    CONSTRAINT acp_session_states_cwd_check
        CHECK (btrim(cwd) <> '' AND octet_length(btrim(cwd)) <= 16384),
    CONSTRAINT acp_session_states_transcript_path_check
        CHECK (
            transcript_path <> ''
            AND octet_length(transcript_path) <= 4096
            AND left(transcript_path, 1) <> '/'
            AND right(transcript_path, 6) = '.jsonl'
            AND position(chr(92) in transcript_path) = 0
            AND transcript_path !~ '(^|/)\.\.?(/|$)'
            AND transcript_path !~ E'[\r\n]'
        ),
    CONSTRAINT acp_session_states_runtime_fencing_token_check
        CHECK (runtime_fencing_token > 0),
    CONSTRAINT acp_session_states_file_count_check
        CHECK (file_count > 0 AND file_count <= 1024),
    CONSTRAINT acp_session_states_record_count_check
        CHECK (record_count > 0 AND record_count <= 2000000),
    CONSTRAINT acp_session_states_file_shapes_check
        CHECK (jsonb_typeof(file_shapes) = 'array')
);

-- Single line set per session: staging appends each file's tail after proving
-- the stored canonical prefix byte-identical. When the proof fails, staging
-- DECLINES without touching canonical rows - the turn publishes a reset head,
-- and only once that reset is canonical may the next turn stage a full
-- rewrite. Lines reference the session directly (not a version header)
-- because versions share them; version membership is defined by the header's
-- file_shapes.
CREATE TABLE IF NOT EXISTS public.acp_session_state_lines (
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
    CONSTRAINT acp_session_state_lines_session_fkey
        FOREIGN KEY (team_id, session_id)
        REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE,
    CONSTRAINT acp_session_state_lines_file_path_check
        CHECK (
            file_path <> ''
            AND octet_length(file_path) <= 4096
            AND left(file_path, 1) <> '/'
            AND right(file_path, 6) = '.jsonl'
            AND position(chr(92) in file_path) = 0
            AND file_path !~ '(^|/)\.\.?(/|$)'
            AND file_path !~ E'[\r\n]'
        ),
    CONSTRAINT acp_session_state_lines_content_size_check
        CHECK (
            content_bytes = octet_length(content)
            AND content_bytes > 0
            AND content_bytes <= 8388608
        ),
    CONSTRAINT acp_session_state_lines_line_number_check
        CHECK (line_number > 0)
);

-- The canonical publication head: one row per session naming the run whose
-- native state the canonical chat history is at. It is written in the same
-- transaction as the round's messages, so a crash can never publish a
-- "ghost transcript" that history does not contain. checkpoint_reset = true
-- means the head is canonical but nothing is resumable (the profile cannot
-- snapshot, or the runtime deliberately started fresh).
CREATE TABLE IF NOT EXISTS public.acp_session_publications (
    team_id          UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                 REFERENCES public.teams(id) ON DELETE RESTRICT,
    session_id       UUID        NOT NULL,
    run_id           UUID        NOT NULL,
    checkpoint_reset BOOLEAN     NOT NULL DEFAULT false,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, session_id),
    CONSTRAINT acp_session_publications_session_fkey
        FOREIGN KEY (team_id, session_id)
        REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE,
    CONSTRAINT acp_session_publications_run_fkey
        FOREIGN KEY (team_id, session_id, run_id)
        REFERENCES public.session_runs(team_id, session_id, run_id) ON DELETE CASCADE
);

ALTER TABLE public.acp_session_publications ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.acp_session_publications FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS acp_session_publications_team_select ON public.acp_session_publications;
DROP POLICY IF EXISTS acp_session_publications_team_insert ON public.acp_session_publications;
DROP POLICY IF EXISTS acp_session_publications_team_update ON public.acp_session_publications;
DROP POLICY IF EXISTS acp_session_publications_team_delete ON public.acp_session_publications;

CREATE POLICY acp_session_publications_team_select ON public.acp_session_publications
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY acp_session_publications_team_insert ON public.acp_session_publications
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY acp_session_publications_team_update ON public.acp_session_publications
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY acp_session_publications_team_delete ON public.acp_session_publications
    FOR DELETE USING (team_id = public.memoh_current_team_id());

ALTER TABLE public.acp_session_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.acp_session_states FORCE ROW LEVEL SECURITY;
ALTER TABLE public.acp_session_state_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.acp_session_state_lines FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS acp_session_states_team_select ON public.acp_session_states;
DROP POLICY IF EXISTS acp_session_states_team_insert ON public.acp_session_states;
DROP POLICY IF EXISTS acp_session_states_team_update ON public.acp_session_states;
DROP POLICY IF EXISTS acp_session_states_team_delete ON public.acp_session_states;
DROP POLICY IF EXISTS acp_session_state_lines_team_select ON public.acp_session_state_lines;
DROP POLICY IF EXISTS acp_session_state_lines_team_insert ON public.acp_session_state_lines;
DROP POLICY IF EXISTS acp_session_state_lines_team_update ON public.acp_session_state_lines;
DROP POLICY IF EXISTS acp_session_state_lines_team_delete ON public.acp_session_state_lines;

CREATE POLICY acp_session_states_team_select ON public.acp_session_states
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY acp_session_states_team_insert ON public.acp_session_states
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY acp_session_states_team_update ON public.acp_session_states
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY acp_session_states_team_delete ON public.acp_session_states
    FOR DELETE USING (team_id = public.memoh_current_team_id());

CREATE POLICY acp_session_state_lines_team_select ON public.acp_session_state_lines
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY acp_session_state_lines_team_insert ON public.acp_session_state_lines
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY acp_session_state_lines_team_update ON public.acp_session_state_lines
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY acp_session_state_lines_team_delete ON public.acp_session_state_lines
    FOR DELETE USING (team_id = public.memoh_current_team_id());
