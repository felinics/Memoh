-- v1 -> v2 bridge: relocate shared types, functions, sequences
-- pgcrypto remains in public; schema_migrations remains in public.

ALTER TYPE public.user_role SET SCHEMA iam;

-- Owned identity/serial sequences move with their tables in 05_move_tables.sql.
-- These two are standalone (no OWNED BY table), so they must be relocated here.
ALTER SEQUENCE public.session_runtime_fencing_token_seq SET SCHEMA agent;
ALTER SEQUENCE public.bot_session_event_cursor_seq SET SCHEMA channel;

ALTER FUNCTION public.memoh_current_team_id() SET SCHEMA iam;
ALTER FUNCTION public.memoh_guard_last_active_team_admin() SET SCHEMA iam;
