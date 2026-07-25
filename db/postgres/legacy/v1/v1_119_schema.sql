-- v1@119 schema snapshot
--
-- The final state of the pre-Epoch schema, used to build a v1 source for
-- exercising the v1 to v2 bridge. It covers both v1 schemas: public, and the
-- template schema holding the provider templates.
--
-- A snapshot is necessary because the frozen v1 ledger cannot be replayed from
-- an empty database. 0001_init.up.sql was maintained as the canonical full
-- schema, so applying it already produces (most of) the end state; the later
-- migrations then either no-op on their idempotent DDL or fail outright when
-- their data-migration statements find their prerequisites gone. Replay also
-- drifts in both directions: 0001 never gained media_assets, tasks, tts_models
-- or tts_providers, and still carries the long-dropped
-- channel_identity_bind_codes.
--
-- Correctness is not asserted by a checksum but proven on every run: the parity
-- test bridges this snapshot to v2 and requires the result to match the fresh v2
-- baselines exactly. A snapshot that misrepresented v1 would fail that
-- comparison.
--
-- v1 has ended, so this file is frozen. Generated with:
--   pg_dump --schema-only --no-owner --no-privileges
-- with pg_dump's \restrict/\unrestrict meta-commands removed so that both psql
-- and database/sql can execute it.
--
-- PostgreSQL database dump
--


-- Dumped from database version 18.4
-- Dumped by pg_dump version 18.4

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: template; Type: SCHEMA; Schema: -; Owner: -
--

CREATE SCHEMA template;


--
-- Name: pgcrypto; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;


--
-- Name: EXTENSION pgcrypto; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION pgcrypto IS 'cryptographic functions';


--
-- Name: user_role; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.user_role AS ENUM (
    'member',
    'admin'
);


--
-- Name: memoh_current_team_id(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.memoh_current_team_id() RETURNS uuid
    LANGUAGE plpgsql STABLE
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
DECLARE
  raw text;
BEGIN
  raw := pg_catalog.current_setting('memoh.team_id', true);
  IF raw IS NULL OR pg_catalog.btrim(raw) = '' THEN
    RAISE EXCEPTION 'memoh.team_id is not set'
      USING ERRCODE = '42501';
  END IF;
  BEGIN
    RETURN raw::uuid;
  EXCEPTION
    WHEN invalid_text_representation THEN
      RAISE EXCEPTION 'memoh.team_id is invalid'
        USING ERRCODE = '22P02';
  END;
END;
$$;


--
-- Name: memoh_guard_last_active_team_admin(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.memoh_guard_last_active_team_admin() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
BEGIN
    IF OLD.role <> 'admin'
       OR NOT OLD.is_active
       OR NOT EXISTS (
           SELECT 1
             FROM public.users principal
            WHERE principal.id = OLD.user_id
              AND principal.is_active
       ) THEN
        IF TG_OP = 'DELETE' THEN
            RETURN OLD;
        END IF;
        RETURN NEW;
    END IF;

    IF TG_OP = 'UPDATE' AND NEW.role = 'admin' AND NEW.is_active THEN
        RETURN NEW;
    END IF;

    -- Concurrent demotions/removals for the same team must observe each other.
    PERFORM 1
      FROM public.teams
     WHERE id = OLD.team_id
     FOR UPDATE;

    IF NOT EXISTS (
        SELECT 1
          FROM public.team_members candidate
          JOIN public.users principal
            ON principal.id = candidate.user_id
           AND principal.is_active
         WHERE candidate.team_id = OLD.team_id
           AND candidate.user_id <> OLD.user_id
           AND candidate.role = 'admin'
           AND candidate.is_active
    ) THEN
        RAISE EXCEPTION 'team must retain at least one active admin'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'team_members_last_active_admin';
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: bot_acl_rules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_acl_rules (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    action text NOT NULL,
    effect text NOT NULL,
    channel_identity_id uuid,
    source_channel text,
    source_conversation_type text,
    source_conversation_id text,
    source_thread_id text,
    created_by_user_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    description text,
    subject_channel_type text,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT bot_acl_rules_action_check CHECK ((action = 'chat.trigger'::text)),
    CONSTRAINT bot_acl_rules_effect_check CHECK ((effect = ANY (ARRAY['allow'::text, 'deny'::text]))),
    CONSTRAINT bot_acl_rules_source_conversation_type_check CHECK (((source_conversation_type IS NULL) OR (source_conversation_type = ANY (ARRAY['private'::text, 'group'::text, 'thread'::text])))),
    CONSTRAINT bot_acl_rules_source_scope_check CHECK ((((source_conversation_id IS NULL) AND (source_thread_id IS NULL)) OR (source_channel IS NOT NULL))),
    CONSTRAINT bot_acl_rules_source_thread_check CHECK (((source_thread_id IS NULL) OR (source_conversation_id IS NOT NULL))),
    CONSTRAINT bot_acl_rules_target_check CHECK (((subject_channel_type IS NULL) OR (btrim(subject_channel_type) <> ''::text)))
);

ALTER TABLE ONLY public.bot_acl_rules FORCE ROW LEVEL SECURITY;


--
-- Name: bot_channel_admins; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_channel_admins (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    channel_identity_id uuid NOT NULL,
    granted boolean DEFAULT true NOT NULL,
    created_by_user_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.bot_channel_admins FORCE ROW LEVEL SECURITY;


--
-- Name: bot_channel_configs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_channel_configs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    channel_type text NOT NULL,
    credentials jsonb DEFAULT '{}'::jsonb NOT NULL,
    external_identity text,
    self_identity jsonb DEFAULT '{}'::jsonb NOT NULL,
    routing jsonb DEFAULT '{}'::jsonb NOT NULL,
    capabilities jsonb DEFAULT '{}'::jsonb NOT NULL,
    disabled boolean DEFAULT false NOT NULL,
    verified_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.bot_channel_configs FORCE ROW LEVEL SECURITY;


--
-- Name: bot_channel_routes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_channel_routes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    channel_type text NOT NULL,
    channel_config_id uuid,
    external_conversation_id text NOT NULL,
    external_thread_id text,
    conversation_type text,
    default_reply_target text,
    active_session_id uuid,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.bot_channel_routes FORCE ROW LEVEL SECURITY;


--
-- Name: bot_email_bindings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_email_bindings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    email_provider_id uuid NOT NULL,
    email_address text NOT NULL,
    can_read boolean DEFAULT true NOT NULL,
    can_write boolean DEFAULT true NOT NULL,
    can_delete boolean DEFAULT false NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.bot_email_bindings FORCE ROW LEVEL SECURITY;


--
-- Name: bot_heartbeat_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_heartbeat_logs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    session_id uuid,
    status text DEFAULT 'ok'::text NOT NULL,
    result_text text DEFAULT ''::text NOT NULL,
    error_message text DEFAULT ''::text NOT NULL,
    usage jsonb,
    model_id uuid,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT bot_heartbeat_logs_status_check CHECK ((status = ANY (ARRAY['ok'::text, 'alert'::text, 'error'::text])))
);

ALTER TABLE ONLY public.bot_heartbeat_logs FORCE ROW LEVEL SECURITY;


--
-- Name: bot_history_message_assets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_history_message_assets (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    message_id uuid NOT NULL,
    role text DEFAULT 'attachment'::text NOT NULL,
    ordinal integer DEFAULT 0 NOT NULL,
    content_hash text NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.bot_history_message_assets FORCE ROW LEVEL SECURITY;


--
-- Name: bot_history_message_compacts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_history_message_compacts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    session_id uuid,
    status text DEFAULT 'pending'::text NOT NULL,
    summary text DEFAULT ''::text NOT NULL,
    message_count integer DEFAULT 0 NOT NULL,
    error_message text DEFAULT ''::text NOT NULL,
    usage jsonb,
    model_id uuid,
    artifact_version integer DEFAULT 1 NOT NULL,
    coverage jsonb DEFAULT '[]'::jsonb NOT NULL,
    anchor_start_ms bigint DEFAULT 0 NOT NULL,
    anchor_end_ms bigint DEFAULT 0 NOT NULL,
    artifact_level integer DEFAULT 0 NOT NULL,
    parent_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    superseded_by uuid,
    superseded_at timestamp with time zone,
    compaction_epoch bigint DEFAULT 0 NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT bot_history_message_compacts_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'ok'::text, 'error'::text])))
);

ALTER TABLE ONLY public.bot_history_message_compacts FORCE ROW LEVEL SECURITY;


--
-- Name: bot_history_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_history_messages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    session_id uuid,
    sender_channel_identity_id uuid,
    sender_account_user_id uuid,
    source_message_id text,
    source_reply_to_message_id text,
    role text NOT NULL,
    content jsonb NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    usage jsonb,
    session_mode text DEFAULT 'chat'::text NOT NULL,
    runtime_type text DEFAULT 'model'::text NOT NULL,
    model_id uuid,
    compact_id uuid,
    event_id uuid,
    display_text text,
    turn_id uuid,
    turn_position bigint,
    turn_message_seq bigint,
    turn_visible boolean DEFAULT false NOT NULL,
    turn_superseded_by_turn_id uuid,
    turn_superseded_at timestamp with time zone,
    turn_superseded_reason text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT bot_history_messages_role_check CHECK ((role = ANY (ARRAY['user'::text, 'assistant'::text, 'system'::text, 'tool'::text]))),
    CONSTRAINT bot_history_messages_runtime_type_check CHECK ((runtime_type = ANY (ARRAY['model'::text, 'acp_agent'::text]))),
    CONSTRAINT bot_history_messages_session_mode_check CHECK ((session_mode = ANY (ARRAY['chat'::text, 'discuss'::text, 'heartbeat'::text, 'schedule'::text, 'subagent'::text])))
);

ALTER TABLE ONLY public.bot_history_messages FORCE ROW LEVEL SECURITY;


--
-- Name: bot_plugin_installations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_plugin_installations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    plugin_id text NOT NULL,
    plugin_name text DEFAULT ''::text NOT NULL,
    version text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'ready'::text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    manifest jsonb DEFAULT '{}'::jsonb NOT NULL,
    installed_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.bot_plugin_installations FORCE ROW LEVEL SECURITY;


--
-- Name: bot_plugin_resources; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_plugin_resources (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    installation_id uuid NOT NULL,
    resource_type text NOT NULL,
    resource_key text NOT NULL,
    resource_id text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.bot_plugin_resources FORCE ROW LEVEL SECURITY;


--
-- Name: bot_remote_runtime_bindings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_remote_runtime_bindings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    runtime_id uuid NOT NULL,
    is_primary boolean DEFAULT false NOT NULL,
    tool_approval_config jsonb DEFAULT '{"exec": {"mode": "ask", "bypass_commands": [], "force_review_commands": []}, "read": {"mode": "allow", "bypass_globs": [], "force_review_globs": []}, "write": {"mode": "ask", "bypass_globs": [], "force_review_globs": []}, "enabled": true}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.bot_remote_runtime_bindings FORCE ROW LEVEL SECURITY;


--
-- Name: bot_session_discuss_cursors; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_session_discuss_cursors (
    session_id uuid NOT NULL,
    scope_key text DEFAULT 'default'::text NOT NULL,
    route_id uuid,
    source text DEFAULT ''::text NOT NULL,
    consumed_cursor bigint DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.bot_session_discuss_cursors FORCE ROW LEVEL SECURITY;


--
-- Name: bot_session_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_session_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    session_id uuid NOT NULL,
    event_kind text NOT NULL,
    event_data jsonb NOT NULL,
    external_message_id text,
    sender_channel_identity_id uuid,
    received_at_ms bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT bot_session_events_event_kind_check CHECK ((event_kind = ANY (ARRAY['message'::text, 'edit'::text, 'delete'::text, 'service'::text])))
);

ALTER TABLE ONLY public.bot_session_events FORCE ROW LEVEL SECURITY;


--
-- Name: bot_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    route_id uuid,
    channel_type text,
    type text DEFAULT 'chat'::text NOT NULL,
    session_mode text DEFAULT 'chat'::text NOT NULL,
    runtime_type text DEFAULT 'model'::text NOT NULL,
    runtime_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    next_turn_position bigint DEFAULT 1 NOT NULL,
    compaction_epoch bigint DEFAULT 0 NOT NULL,
    runtime_fencing_token bigint DEFAULT 0 NOT NULL,
    parent_session_id uuid,
    created_by_user_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT bot_sessions_runtime_fencing_token_check CHECK ((runtime_fencing_token >= 0)),
    CONSTRAINT bot_sessions_runtime_type_check CHECK ((runtime_type = ANY (ARRAY['model'::text, 'acp_agent'::text]))),
    CONSTRAINT bot_sessions_session_mode_check CHECK ((session_mode = ANY (ARRAY['chat'::text, 'discuss'::text, 'heartbeat'::text, 'schedule'::text, 'subagent'::text]))),
    CONSTRAINT bot_sessions_type_check CHECK ((type = ANY (ARRAY['chat'::text, 'heartbeat'::text, 'schedule'::text, 'subagent'::text, 'discuss'::text, 'acp_agent'::text])))
);

ALTER TABLE ONLY public.bot_sessions FORCE ROW LEVEL SECURITY;


--
-- Name: bot_storage_bindings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_storage_bindings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    storage_provider_id uuid NOT NULL,
    base_path text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.bot_storage_bindings FORCE ROW LEVEL SECURITY;


--
-- Name: bot_user_grants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_user_grants (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    subject_type text NOT NULL,
    user_id uuid,
    permissions jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_by_user_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT bot_user_grants_subject_type_check CHECK ((subject_type = ANY (ARRAY['user'::text, 'everyone'::text]))),
    CONSTRAINT bot_user_grants_subject_value_check CHECK ((((subject_type = 'user'::text) AND (user_id IS NOT NULL)) OR ((subject_type = 'everyone'::text) AND (user_id IS NULL))))
);

ALTER TABLE ONLY public.bot_user_grants FORCE ROW LEVEL SECURITY;


--
-- Name: bot_visible_history_messages; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.bot_visible_history_messages WITH (security_invoker='true') AS
 SELECT team_id,
    turn_id,
    turn_position,
    turn_message_seq,
    id,
    bot_id,
    session_id,
    sender_channel_identity_id,
    sender_account_user_id,
    source_message_id,
    source_reply_to_message_id,
    role,
    content,
    metadata,
    usage,
    compact_id,
    session_mode,
    runtime_type,
    model_id,
    event_id,
    display_text,
    created_at
   FROM public.bot_history_messages m
  WHERE ((turn_visible = true) AND (turn_id IS NOT NULL) AND (turn_position IS NOT NULL) AND (turn_message_seq IS NOT NULL));


--
-- Name: bot_workspace_resource_limits; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_workspace_resource_limits (
    bot_id uuid NOT NULL,
    cpu_millicores bigint DEFAULT 0 NOT NULL,
    memory_bytes bigint DEFAULT 0 NOT NULL,
    storage_bytes bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT bot_workspace_resource_limits_cpu_check CHECK ((cpu_millicores >= 0)),
    CONSTRAINT bot_workspace_resource_limits_memory_check CHECK ((memory_bytes >= 0)),
    CONSTRAINT bot_workspace_resource_limits_storage_check CHECK ((storage_bytes >= 0))
);

ALTER TABLE ONLY public.bot_workspace_resource_limits FORCE ROW LEVEL SECURITY;


--
-- Name: bots; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bots (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_user_id uuid NOT NULL,
    name text NOT NULL,
    display_name text,
    avatar_url text,
    timezone text,
    is_active boolean DEFAULT true NOT NULL,
    status text DEFAULT 'ready'::text NOT NULL,
    language text DEFAULT 'auto'::text NOT NULL,
    command_ui_language text DEFAULT 'auto'::text NOT NULL,
    reasoning_enabled boolean DEFAULT false NOT NULL,
    reasoning_effort text DEFAULT 'medium'::text NOT NULL,
    chat_model_id uuid,
    chat_runtime text DEFAULT 'model'::text NOT NULL,
    chat_acp_agent_id text,
    chat_acp_project_path text DEFAULT '/data'::text NOT NULL,
    chat_acp_project_mode text DEFAULT 'project'::text NOT NULL,
    search_provider_id uuid,
    fetch_provider_id uuid,
    memory_provider_id uuid,
    heartbeat_enabled boolean DEFAULT false NOT NULL,
    heartbeat_interval integer DEFAULT 1440 NOT NULL,
    heartbeat_prompt text DEFAULT ''::text NOT NULL,
    heartbeat_model_id uuid,
    compaction_enabled boolean DEFAULT false NOT NULL,
    compaction_threshold integer DEFAULT 100000 NOT NULL,
    compaction_ratio integer DEFAULT 80 NOT NULL,
    compaction_model_id uuid,
    image_model_id uuid,
    discuss_probe_model_id uuid,
    tts_model_id uuid,
    transcription_model_id uuid,
    video_model_id uuid,
    persist_full_tool_results boolean DEFAULT false NOT NULL,
    show_tool_calls_in_im boolean DEFAULT false NOT NULL,
    tool_approval_config jsonb DEFAULT '{"exec": {"bypass_commands": [], "require_approval": false, "force_review_commands": []}, "read": {"bypass_globs": [], "require_approval": false, "force_review_globs": []}, "write": {"bypass_globs": ["/data/**", "/tmp/**"], "require_approval": true, "force_review_globs": []}, "enabled": false}'::jsonb NOT NULL,
    display_enabled boolean DEFAULT false NOT NULL,
    overlay_provider text DEFAULT ''::text NOT NULL,
    overlay_enabled boolean DEFAULT false NOT NULL,
    overlay_config jsonb DEFAULT '{}'::jsonb NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    acl_default_effect text DEFAULT 'allow'::text NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT bots_acl_default_effect_check CHECK ((acl_default_effect = ANY (ARRAY['allow'::text, 'deny'::text]))),
    CONSTRAINT bots_chat_acp_project_mode_check CHECK ((chat_acp_project_mode = ANY (ARRAY['project'::text, 'none'::text]))),
    CONSTRAINT bots_chat_runtime_check CHECK ((chat_runtime = ANY (ARRAY['model'::text, 'acp_agent'::text]))),
    CONSTRAINT bots_name_format_check CHECK ((name ~ '^[a-z0-9][a-z0-9-]{1,62}$'::text)),
    CONSTRAINT bots_status_check CHECK ((status = ANY (ARRAY['creating'::text, 'ready'::text, 'deleting'::text])))
);

ALTER TABLE ONLY public.bots FORCE ROW LEVEL SECURITY;


--
-- Name: channel_identities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_identities (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    channel_type text NOT NULL,
    channel_subject_id text NOT NULL,
    display_name text,
    avatar_url text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.channel_identities FORCE ROW LEVEL SECURITY;


--
-- Name: channel_link_codes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_link_codes (
    token text NOT NULL,
    user_id uuid NOT NULL,
    channel_type text DEFAULT ''::text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    consumed_channel_identity_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.channel_link_codes FORCE ROW LEVEL SECURITY;


--
-- Name: container_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.container_versions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    container_id text NOT NULL,
    snapshot_id uuid NOT NULL,
    version integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.container_versions FORCE ROW LEVEL SECURITY;


--
-- Name: containers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.containers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    container_id text NOT NULL,
    container_name text NOT NULL,
    image text NOT NULL,
    status text DEFAULT 'created'::text NOT NULL,
    namespace text DEFAULT 'default'::text NOT NULL,
    auto_start boolean DEFAULT true NOT NULL,
    container_path text DEFAULT '/data'::text NOT NULL,
    workspace_backend text DEFAULT 'container'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    last_started_at timestamp with time zone,
    last_stopped_at timestamp with time zone,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.containers FORCE ROW LEVEL SECURITY;


--
-- Name: email_oauth_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_oauth_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email_provider_id uuid NOT NULL,
    email_address text DEFAULT ''::text NOT NULL,
    access_token text DEFAULT ''::text NOT NULL,
    refresh_token text DEFAULT ''::text NOT NULL,
    expires_at timestamp with time zone,
    scope text DEFAULT ''::text NOT NULL,
    state text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.email_oauth_tokens FORCE ROW LEVEL SECURITY;


--
-- Name: email_outbox; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_outbox (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider_id uuid NOT NULL,
    bot_id uuid NOT NULL,
    message_id text DEFAULT ''::text NOT NULL,
    from_address text DEFAULT ''::text NOT NULL,
    to_addresses jsonb DEFAULT '[]'::jsonb NOT NULL,
    subject text DEFAULT ''::text NOT NULL,
    body_text text DEFAULT ''::text NOT NULL,
    body_html text DEFAULT ''::text NOT NULL,
    attachments jsonb DEFAULT '[]'::jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    sent_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT email_outbox_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'sent'::text, 'failed'::text])))
);

ALTER TABLE ONLY public.email_outbox FORCE ROW LEVEL SECURITY;


--
-- Name: email_providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.email_providers FORCE ROW LEVEL SECURITY;


--
-- Name: fetch_providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fetch_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    enable boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.fetch_providers FORCE ROW LEVEL SECURITY;


--
-- Name: lifecycle_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.lifecycle_events (
    id text NOT NULL,
    container_id text NOT NULL,
    event_type text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.lifecycle_events FORCE ROW LEVEL SECURITY;


--
-- Name: mcp_connections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.mcp_connections (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    name text NOT NULL,
    type text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    status text DEFAULT 'unknown'::text NOT NULL,
    tools_cache jsonb DEFAULT '[]'::jsonb NOT NULL,
    last_probed_at timestamp with time zone,
    status_message text DEFAULT ''::text NOT NULL,
    auth_type text DEFAULT 'none'::text NOT NULL,
    managed_by_plugin_installation_id uuid,
    managed_resource_key text DEFAULT ''::text NOT NULL,
    visible boolean DEFAULT true NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT mcp_connections_type_check CHECK ((type = ANY (ARRAY['stdio'::text, 'http'::text, 'sse'::text])))
);

ALTER TABLE ONLY public.mcp_connections FORCE ROW LEVEL SECURITY;


--
-- Name: mcp_oauth_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.mcp_oauth_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    connection_id uuid NOT NULL,
    resource_metadata_url text DEFAULT ''::text NOT NULL,
    authorization_server_url text DEFAULT ''::text NOT NULL,
    authorization_endpoint text DEFAULT ''::text NOT NULL,
    token_endpoint text DEFAULT ''::text NOT NULL,
    registration_endpoint text DEFAULT ''::text NOT NULL,
    scopes_supported text[] DEFAULT '{}'::text[] NOT NULL,
    client_id text DEFAULT ''::text NOT NULL,
    client_secret text DEFAULT ''::text NOT NULL,
    access_token text DEFAULT ''::text NOT NULL,
    refresh_token text DEFAULT ''::text NOT NULL,
    token_type text DEFAULT 'Bearer'::text NOT NULL,
    expires_at timestamp with time zone,
    scope text DEFAULT ''::text NOT NULL,
    pkce_code_verifier text DEFAULT ''::text NOT NULL,
    state_param text DEFAULT ''::text NOT NULL,
    resource_uri text DEFAULT ''::text NOT NULL,
    redirect_uri text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.mcp_oauth_tokens FORCE ROW LEVEL SECURITY;


--
-- Name: media_assets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.media_assets (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    storage_provider_id uuid,
    content_hash text NOT NULL,
    media_type text NOT NULL,
    mime text DEFAULT 'application/octet-stream'::text NOT NULL,
    size_bytes bigint DEFAULT 0 NOT NULL,
    storage_key text NOT NULL,
    original_name text,
    width integer,
    height integer,
    duration_ms bigint,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.media_assets FORCE ROW LEVEL SECURITY;


--
-- Name: memory_edges; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.memory_edges (
    id bigint NOT NULL,
    bot_id uuid NOT NULL,
    src_node text NOT NULL,
    dst_node text NOT NULL,
    rel text NOT NULL,
    weight real DEFAULT 1.0 NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.memory_edges FORCE ROW LEVEL SECURITY;


--
-- Name: memory_edges_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.memory_edges_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: memory_edges_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.memory_edges_id_seq OWNED BY public.memory_edges.id;


--
-- Name: memory_nodes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.memory_nodes (
    id text NOT NULL,
    bot_id uuid NOT NULL,
    body text NOT NULL,
    hash text NOT NULL,
    layer text DEFAULT 'note'::text NOT NULL,
    fact_type text DEFAULT ''::text NOT NULL,
    subject text DEFAULT ''::text NOT NULL,
    confidence real DEFAULT 0.5 NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    source_message_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    profile_ref text DEFAULT ''::text NOT NULL,
    topic text DEFAULT ''::text NOT NULL,
    captured_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT memory_nodes_confidence_check CHECK (((confidence >= (0)::double precision) AND (confidence <= (1)::double precision)))
);

ALTER TABLE ONLY public.memory_nodes FORCE ROW LEVEL SECURITY;


--
-- Name: memory_providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.memory_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.memory_providers FORCE ROW LEVEL SECURITY;


--
-- Name: model_variants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_variants (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    model_uuid uuid NOT NULL,
    variant_id text NOT NULL,
    weight integer NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.model_variants FORCE ROW LEVEL SECURITY;


--
-- Name: models; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.models (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    model_id text NOT NULL,
    name text,
    provider_id uuid NOT NULL,
    type text DEFAULT 'chat'::text NOT NULL,
    enable boolean DEFAULT true NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT models_type_check CHECK ((type = ANY (ARRAY['chat'::text, 'embedding'::text, 'speech'::text, 'transcription'::text, 'video'::text])))
);

ALTER TABLE ONLY public.models FORCE ROW LEVEL SECURITY;


--
-- Name: provider_oauth_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_oauth_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider_id uuid NOT NULL,
    access_token text DEFAULT ''::text NOT NULL,
    refresh_token text DEFAULT ''::text NOT NULL,
    expires_at timestamp with time zone,
    scope text DEFAULT ''::text NOT NULL,
    token_type text DEFAULT ''::text NOT NULL,
    state text DEFAULT ''::text NOT NULL,
    pkce_code_verifier text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.provider_oauth_tokens FORCE ROW LEVEL SECURITY;


--
-- Name: providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider_template_id uuid,
    name text NOT NULL,
    client_type text DEFAULT 'openai-completions'::text NOT NULL,
    icon text,
    enable boolean DEFAULT true NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT providers_client_type_check CHECK ((client_type = ANY (ARRAY['openai-responses'::text, 'openai-completions'::text, 'anthropic-messages'::text, 'google-generative-ai'::text, 'openai-codex'::text, 'github-copilot'::text, 'edge-speech'::text, 'openai-speech'::text, 'openai-transcription'::text, 'openrouter-speech'::text, 'openrouter-transcription'::text, 'elevenlabs-speech'::text, 'elevenlabs-transcription'::text, 'deepgram-speech'::text, 'deepgram-transcription'::text, 'minimax-speech'::text, 'volcengine-speech'::text, 'alibabacloud-speech'::text, 'microsoft-speech'::text, 'google-speech'::text, 'google-transcription'::text, 'openrouter-video'::text, 'modelark-video'::text, 'volcengine-video'::text])))
);

ALTER TABLE ONLY public.providers FORCE ROW LEVEL SECURITY;


--
-- Name: schedule; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schedule (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    description text NOT NULL,
    pattern text NOT NULL,
    max_calls integer,
    current_calls integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    command text NOT NULL,
    bot_id uuid NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.schedule FORCE ROW LEVEL SECURITY;


--
-- Name: schedule_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schedule_logs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    schedule_id uuid NOT NULL,
    bot_id uuid NOT NULL,
    session_id uuid,
    status text DEFAULT 'ok'::text NOT NULL,
    result_text text DEFAULT ''::text NOT NULL,
    error_message text DEFAULT ''::text NOT NULL,
    usage jsonb,
    model_id uuid,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT schedule_logs_status_check CHECK ((status = ANY (ARRAY['ok'::text, 'error'::text])))
);

ALTER TABLE ONLY public.schedule_logs FORCE ROW LEVEL SECURITY;


--
-- Name: schema_migrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schema_migrations (
    version bigint NOT NULL,
    dirty boolean NOT NULL
);


--
-- Name: search_providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.search_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    enable boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.search_providers FORCE ROW LEVEL SECURITY;


--
-- Name: session_runtime_fencing_token_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.session_runtime_fencing_token_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: snapshots; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.snapshots (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    container_id text NOT NULL,
    runtime_snapshot_name text NOT NULL,
    display_name text,
    parent_runtime_snapshot_name text,
    snapshotter text NOT NULL,
    source text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.snapshots FORCE ROW LEVEL SECURITY;


--
-- Name: storage_providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.storage_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT storage_providers_provider_check CHECK ((provider = ANY (ARRAY['localfs'::text, 's3'::text, 'gcs'::text])))
);

ALTER TABLE ONLY public.storage_providers FORCE ROW LEVEL SECURITY;


--
-- Name: subagent_configs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.subagent_configs (
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    session_id uuid NOT NULL,
    model_uuid uuid,
    model_id text NOT NULL,
    provider_name text NOT NULL,
    forked boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.subagent_configs FORCE ROW LEVEL SECURITY;


--
-- Name: tasks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tasks (
    id character varying(255) NOT NULL,
    bot_id character varying(255) NOT NULL,
    name character varying(255) NOT NULL,
    command text NOT NULL,
    status character varying(50) DEFAULT 'pending'::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    exec_id character varying(255),
    pid integer,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.tasks FORCE ROW LEVEL SECURITY;


--
-- Name: team_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.team_members (
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    user_id uuid NOT NULL,
    role public.user_role DEFAULT 'member'::public.user_role NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    data_root text,
    title_model_id uuid,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.team_members FORCE ROW LEVEL SECURITY;


--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    username text,
    email text,
    password_hash text,
    display_name text,
    avatar_url text,
    timezone text DEFAULT 'UTC'::text NOT NULL,
    last_login_at timestamp with time zone,
    is_active boolean DEFAULT true NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: team_accounts; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.team_accounts WITH (security_invoker='true') AS
 SELECT u.id,
    u.username,
    u.email,
    u.password_hash,
    tm.role,
    u.display_name,
    u.avatar_url,
    u.timezone,
    tm.data_root,
    u.last_login_at,
    (u.is_active AND tm.is_active) AS is_active,
    u.metadata,
    u.created_at,
    u.updated_at,
    tm.team_id,
    u.is_active AS principal_is_active,
    tm.is_active AS membership_is_active,
    tm.created_at AS joined_at,
    tm.updated_at AS membership_updated_at,
    tm.title_model_id
   FROM (public.team_members tm
     JOIN public.users u ON ((u.id = tm.user_id)))
  WHERE (tm.team_id = public.memoh_current_team_id());


--
-- Name: teams; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.teams (
    id uuid NOT NULL,
    slug text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL
);

ALTER TABLE ONLY public.teams FORCE ROW LEVEL SECURITY;


--
-- Name: tool_approval_requests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_approval_requests (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    session_id uuid NOT NULL,
    route_id uuid,
    channel_identity_id uuid,
    workspace_target_id text DEFAULT ''::text NOT NULL,
    tool_call_id text NOT NULL,
    tool_name text NOT NULL,
    operation text NOT NULL,
    tool_input jsonb NOT NULL,
    short_id integer NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    runtime_fencing_token bigint,
    decision_reason text DEFAULT ''::text NOT NULL,
    requested_by_channel_identity_id uuid,
    decided_by_channel_identity_id uuid,
    requested_message_id uuid,
    prompt_message_id uuid,
    prompt_external_message_id text DEFAULT ''::text NOT NULL,
    source_platform text DEFAULT ''::text NOT NULL,
    reply_target text DEFAULT ''::text NOT NULL,
    conversation_type text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    decided_at timestamp with time zone,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT tool_approval_operation_check CHECK ((operation = ANY (ARRAY['read'::text, 'write'::text, 'exec'::text]))),
    CONSTRAINT tool_approval_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text, 'expired'::text, 'cancelled'::text])))
);

ALTER TABLE ONLY public.tool_approval_requests FORCE ROW LEVEL SECURITY;


--
-- Name: tts_models; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tts_models (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    model_id text NOT NULL,
    name text,
    tts_provider_id uuid NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.tts_models FORCE ROW LEVEL SECURITY;


--
-- Name: tts_providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tts_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    enable boolean DEFAULT false NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.tts_providers FORCE ROW LEVEL SECURITY;


--
-- Name: user_channel_bindings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_channel_bindings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    channel_type text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.user_channel_bindings FORCE ROW LEVEL SECURITY;


--
-- Name: user_channel_identity_bindings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_channel_identity_bindings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    channel_identity_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.user_channel_identity_bindings FORCE ROW LEVEL SECURITY;


--
-- Name: user_input_requests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_input_requests (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bot_id uuid NOT NULL,
    session_id uuid NOT NULL,
    route_id uuid,
    channel_identity_id uuid,
    workspace_target_id text DEFAULT ''::text NOT NULL,
    tool_call_id text NOT NULL,
    tool_name text DEFAULT 'ask_user'::text NOT NULL,
    short_id integer NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    runtime_fencing_token bigint,
    input_json jsonb NOT NULL,
    ui_payload_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    interaction_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    interaction_revision integer DEFAULT 0 NOT NULL,
    result_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    provider_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    requested_by_channel_identity_id uuid,
    responded_by_channel_identity_id uuid,
    assistant_message_id uuid,
    tool_result_message_id uuid,
    prompt_message_id uuid,
    prompt_external_message_id text DEFAULT ''::text NOT NULL,
    source_platform text DEFAULT ''::text NOT NULL,
    reply_target text DEFAULT ''::text NOT NULL,
    conversation_type text DEFAULT ''::text NOT NULL,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    responded_at timestamp with time zone,
    canceled_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT user_input_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'submitted'::text, 'canceled'::text, 'expired'::text, 'failed'::text]))),
    CONSTRAINT user_input_tool_name_check CHECK ((tool_name = 'ask_user'::text))
);

ALTER TABLE ONLY public.user_input_requests FORCE ROW LEVEL SECURITY;


--
-- Name: user_provider_oauth_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_provider_oauth_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider_id uuid NOT NULL,
    user_id uuid NOT NULL,
    access_token text DEFAULT ''::text NOT NULL,
    refresh_token text DEFAULT ''::text NOT NULL,
    expires_at timestamp with time zone,
    scope text DEFAULT ''::text NOT NULL,
    token_type text DEFAULT ''::text NOT NULL,
    state text DEFAULT ''::text NOT NULL,
    pkce_code_verifier text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY public.user_provider_oauth_tokens FORCE ROW LEVEL SECURITY;


--
-- Name: user_runtimes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_runtimes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    name text NOT NULL,
    api_token text NOT NULL,
    revoked_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT user_runtimes_name_check CHECK ((btrim(name) <> ''::text))
);

ALTER TABLE ONLY public.user_runtimes FORCE ROW LEVEL SECURITY;


--
-- Name: provider_template_models; Type: TABLE; Schema: template; Owner: -
--

CREATE TABLE template.provider_template_models (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider_template_id uuid NOT NULL,
    model_id text NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    type text DEFAULT 'chat'::text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_template_models_type_check CHECK ((type = ANY (ARRAY['chat'::text, 'embedding'::text, 'speech'::text, 'transcription'::text, 'video'::text])))
);


--
-- Name: provider_templates; Type: TABLE; Schema: template; Owner: -
--

CREATE TABLE template.provider_templates (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    key text NOT NULL,
    domain text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    icon text,
    driver text NOT NULL,
    config_schema jsonb DEFAULT '{}'::jsonb NOT NULL,
    default_config jsonb DEFAULT '{}'::jsonb NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    source text DEFAULT ''::text NOT NULL,
    content_hash text NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_templates_domain_check CHECK ((domain = ANY (ARRAY['llm'::text, 'speech'::text, 'transcription'::text, 'video'::text])))
);


--
-- Name: memory_edges id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory_edges ALTER COLUMN id SET DEFAULT nextval('public.memory_edges_id_seq'::regclass);


--
-- Name: bot_acl_rules bot_acl_rules_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_acl_rules
    ADD CONSTRAINT bot_acl_rules_pkey PRIMARY KEY (id);


--
-- Name: bot_acl_rules bot_acl_rules_unique_target; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_acl_rules
    ADD CONSTRAINT bot_acl_rules_unique_target UNIQUE NULLS NOT DISTINCT (team_id, bot_id, action, effect, channel_identity_id, subject_channel_type, source_conversation_type, source_conversation_id, source_thread_id);


--
-- Name: bot_channel_admins bot_channel_admins_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_admins
    ADD CONSTRAINT bot_channel_admins_pkey PRIMARY KEY (id);


--
-- Name: bot_channel_admins bot_channel_admins_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_admins
    ADD CONSTRAINT bot_channel_admins_unique UNIQUE (team_id, bot_id, channel_identity_id);


--
-- Name: bot_channel_configs bot_channel_configs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_configs
    ADD CONSTRAINT bot_channel_configs_pkey PRIMARY KEY (id);


--
-- Name: bot_channel_routes bot_channel_routes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_routes
    ADD CONSTRAINT bot_channel_routes_pkey PRIMARY KEY (id);


--
-- Name: bot_channel_configs bot_channel_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_configs
    ADD CONSTRAINT bot_channel_unique UNIQUE (team_id, bot_id, channel_type);


--
-- Name: bot_email_bindings bot_email_bindings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_email_bindings
    ADD CONSTRAINT bot_email_bindings_pkey PRIMARY KEY (id);


--
-- Name: bot_email_bindings bot_email_bindings_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_email_bindings
    ADD CONSTRAINT bot_email_bindings_unique UNIQUE (team_id, bot_id, email_provider_id);


--
-- Name: bot_heartbeat_logs bot_heartbeat_logs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_heartbeat_logs
    ADD CONSTRAINT bot_heartbeat_logs_pkey PRIMARY KEY (id);


--
-- Name: bot_history_message_assets bot_history_message_assets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_message_assets
    ADD CONSTRAINT bot_history_message_assets_pkey PRIMARY KEY (id);


--
-- Name: bot_history_message_compacts bot_history_message_compacts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_message_compacts
    ADD CONSTRAINT bot_history_message_compacts_pkey PRIMARY KEY (id);


--
-- Name: bot_history_messages bot_history_messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_messages
    ADD CONSTRAINT bot_history_messages_pkey PRIMARY KEY (id);


--
-- Name: bot_plugin_installations bot_plugin_installations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_plugin_installations
    ADD CONSTRAINT bot_plugin_installations_pkey PRIMARY KEY (id);


--
-- Name: bot_plugin_installations bot_plugin_installations_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_plugin_installations
    ADD CONSTRAINT bot_plugin_installations_unique UNIQUE (team_id, bot_id, plugin_id);


--
-- Name: bot_plugin_resources bot_plugin_resources_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_plugin_resources
    ADD CONSTRAINT bot_plugin_resources_pkey PRIMARY KEY (id);


--
-- Name: bot_plugin_resources bot_plugin_resources_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_plugin_resources
    ADD CONSTRAINT bot_plugin_resources_unique UNIQUE (team_id, installation_id, resource_type, resource_key);


--
-- Name: bot_remote_runtime_bindings bot_remote_runtime_bindings_bot_id_runtime_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_remote_runtime_bindings
    ADD CONSTRAINT bot_remote_runtime_bindings_bot_id_runtime_id_key UNIQUE (team_id, bot_id, runtime_id);


--
-- Name: bot_remote_runtime_bindings bot_remote_runtime_bindings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_remote_runtime_bindings
    ADD CONSTRAINT bot_remote_runtime_bindings_pkey PRIMARY KEY (id);


--
-- Name: bot_session_discuss_cursors bot_session_discuss_cursors_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_session_discuss_cursors
    ADD CONSTRAINT bot_session_discuss_cursors_pkey PRIMARY KEY (session_id, scope_key);


--
-- Name: bot_session_events bot_session_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_session_events
    ADD CONSTRAINT bot_session_events_pkey PRIMARY KEY (id);


--
-- Name: bot_sessions bot_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_sessions
    ADD CONSTRAINT bot_sessions_pkey PRIMARY KEY (id);


--
-- Name: bot_storage_bindings bot_storage_bindings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_storage_bindings
    ADD CONSTRAINT bot_storage_bindings_pkey PRIMARY KEY (id);


--
-- Name: bot_storage_bindings bot_storage_bindings_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_storage_bindings
    ADD CONSTRAINT bot_storage_bindings_unique UNIQUE (team_id, bot_id);


--
-- Name: bot_user_grants bot_user_grants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_user_grants
    ADD CONSTRAINT bot_user_grants_pkey PRIMARY KEY (id);


--
-- Name: bot_workspace_resource_limits bot_workspace_resource_limits_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_workspace_resource_limits
    ADD CONSTRAINT bot_workspace_resource_limits_pkey PRIMARY KEY (bot_id);


--
-- Name: bots bots_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_pkey PRIMARY KEY (id);


--
-- Name: channel_identities channel_identities_channel_type_subject_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_identities
    ADD CONSTRAINT channel_identities_channel_type_subject_unique UNIQUE (team_id, channel_type, channel_subject_id);


--
-- Name: channel_identities channel_identities_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_identities
    ADD CONSTRAINT channel_identities_pkey PRIMARY KEY (id);


--
-- Name: channel_link_codes channel_link_codes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_link_codes
    ADD CONSTRAINT channel_link_codes_pkey PRIMARY KEY (token);


--
-- Name: container_versions container_versions_container_id_version_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.container_versions
    ADD CONSTRAINT container_versions_container_id_version_key UNIQUE (team_id, container_id, version);


--
-- Name: container_versions container_versions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.container_versions
    ADD CONSTRAINT container_versions_pkey PRIMARY KEY (id);


--
-- Name: containers containers_container_id_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.containers
    ADD CONSTRAINT containers_container_id_unique UNIQUE (team_id, container_id);


--
-- Name: containers containers_container_name_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.containers
    ADD CONSTRAINT containers_container_name_unique UNIQUE (team_id, container_name);


--
-- Name: containers containers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.containers
    ADD CONSTRAINT containers_pkey PRIMARY KEY (id);


--
-- Name: email_oauth_tokens email_oauth_tokens_email_provider_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_oauth_tokens
    ADD CONSTRAINT email_oauth_tokens_email_provider_id_key UNIQUE (team_id, email_provider_id);


--
-- Name: email_oauth_tokens email_oauth_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_oauth_tokens
    ADD CONSTRAINT email_oauth_tokens_pkey PRIMARY KEY (id);


--
-- Name: email_outbox email_outbox_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_outbox
    ADD CONSTRAINT email_outbox_pkey PRIMARY KEY (id);


--
-- Name: email_providers email_providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_providers
    ADD CONSTRAINT email_providers_pkey PRIMARY KEY (id);


--
-- Name: email_providers email_providers_user_name_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_providers
    ADD CONSTRAINT email_providers_user_name_unique UNIQUE (team_id, user_id, name);


--
-- Name: fetch_providers fetch_providers_name_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fetch_providers
    ADD CONSTRAINT fetch_providers_name_unique UNIQUE (team_id, name);


--
-- Name: fetch_providers fetch_providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fetch_providers
    ADD CONSTRAINT fetch_providers_pkey PRIMARY KEY (id);


--
-- Name: lifecycle_events lifecycle_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.lifecycle_events
    ADD CONSTRAINT lifecycle_events_pkey PRIMARY KEY (id);


--
-- Name: mcp_connections mcp_connections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.mcp_connections
    ADD CONSTRAINT mcp_connections_pkey PRIMARY KEY (id);


--
-- Name: mcp_connections mcp_connections_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.mcp_connections
    ADD CONSTRAINT mcp_connections_unique UNIQUE (team_id, bot_id, name);


--
-- Name: mcp_oauth_tokens mcp_oauth_tokens_connection_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.mcp_oauth_tokens
    ADD CONSTRAINT mcp_oauth_tokens_connection_id_key UNIQUE (team_id, connection_id);


--
-- Name: mcp_oauth_tokens mcp_oauth_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.mcp_oauth_tokens
    ADD CONSTRAINT mcp_oauth_tokens_pkey PRIMARY KEY (id);


--
-- Name: media_assets media_assets_bot_hash_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_assets
    ADD CONSTRAINT media_assets_bot_hash_unique UNIQUE (team_id, bot_id, content_hash);


--
-- Name: media_assets media_assets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_assets
    ADD CONSTRAINT media_assets_pkey PRIMARY KEY (id);


--
-- Name: user_channel_identity_bindings memoh_team_key_013e28c14a2d; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_channel_identity_bindings
    ADD CONSTRAINT memoh_team_key_013e28c14a2d UNIQUE (team_id, id);


--
-- Name: mcp_oauth_tokens memoh_team_key_02f43ca206ff; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.mcp_oauth_tokens
    ADD CONSTRAINT memoh_team_key_02f43ca206ff UNIQUE (team_id, id);


--
-- Name: email_oauth_tokens memoh_team_key_0b525bc25d91; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_oauth_tokens
    ADD CONSTRAINT memoh_team_key_0b525bc25d91 UNIQUE (team_id, id);


--
-- Name: bot_workspace_resource_limits memoh_team_key_1ea7647190ff; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_workspace_resource_limits
    ADD CONSTRAINT memoh_team_key_1ea7647190ff UNIQUE (team_id, bot_id);


--
-- Name: user_input_requests memoh_team_key_25351826f141; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_input_requests
    ADD CONSTRAINT memoh_team_key_25351826f141 UNIQUE (team_id, id);


--
-- Name: bot_plugin_resources memoh_team_key_288ff97bb452; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_plugin_resources
    ADD CONSTRAINT memoh_team_key_288ff97bb452 UNIQUE (team_id, id);


--
-- Name: memory_nodes memoh_team_key_354c9a71ade3; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory_nodes
    ADD CONSTRAINT memoh_team_key_354c9a71ade3 UNIQUE (team_id, id);


--
-- Name: lifecycle_events memoh_team_key_38bce59cf848; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.lifecycle_events
    ADD CONSTRAINT memoh_team_key_38bce59cf848 UNIQUE (team_id, id);


--
-- Name: mcp_connections memoh_team_key_3a7ac918eb09; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.mcp_connections
    ADD CONSTRAINT memoh_team_key_3a7ac918eb09 UNIQUE (team_id, id);


--
-- Name: models memoh_team_key_3b9411fe39b8; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.models
    ADD CONSTRAINT memoh_team_key_3b9411fe39b8 UNIQUE (team_id, id);


--
-- Name: providers memoh_team_key_3ff2063985e5; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.providers
    ADD CONSTRAINT memoh_team_key_3ff2063985e5 UNIQUE (team_id, id);


--
-- Name: email_providers memoh_team_key_41adeda78ae7; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_providers
    ADD CONSTRAINT memoh_team_key_41adeda78ae7 UNIQUE (team_id, id);


--
-- Name: memory_edges memoh_team_key_483f14eea5aa; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory_edges
    ADD CONSTRAINT memoh_team_key_483f14eea5aa UNIQUE (team_id, id);


--
-- Name: bot_session_discuss_cursors memoh_team_key_48c0ed6bd276; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_session_discuss_cursors
    ADD CONSTRAINT memoh_team_key_48c0ed6bd276 UNIQUE (team_id, session_id, scope_key);


--
-- Name: container_versions memoh_team_key_4e4a7126f607; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.container_versions
    ADD CONSTRAINT memoh_team_key_4e4a7126f607 UNIQUE (team_id, id);


--
-- Name: search_providers memoh_team_key_512d51a6668f; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_providers
    ADD CONSTRAINT memoh_team_key_512d51a6668f UNIQUE (team_id, id);


--
-- Name: tts_models memoh_team_key_5a0d5daa7bc4; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tts_models
    ADD CONSTRAINT memoh_team_key_5a0d5daa7bc4 UNIQUE (team_id, id);


--
-- Name: storage_providers memoh_team_key_5d8907827b81; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_providers
    ADD CONSTRAINT memoh_team_key_5d8907827b81 UNIQUE (team_id, id);


--
-- Name: fetch_providers memoh_team_key_60114d54331f; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fetch_providers
    ADD CONSTRAINT memoh_team_key_60114d54331f UNIQUE (team_id, id);


--
-- Name: bot_plugin_installations memoh_team_key_640e5c8de6b5; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_plugin_installations
    ADD CONSTRAINT memoh_team_key_640e5c8de6b5 UNIQUE (team_id, id);


--
-- Name: user_provider_oauth_tokens memoh_team_key_65ba82d99f4b; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_provider_oauth_tokens
    ADD CONSTRAINT memoh_team_key_65ba82d99f4b UNIQUE (team_id, id);


--
-- Name: snapshots memoh_team_key_6bceec3b7fb5; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.snapshots
    ADD CONSTRAINT memoh_team_key_6bceec3b7fb5 UNIQUE (team_id, id);


--
-- Name: bot_history_messages memoh_team_key_6e575bdde4a7; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_messages
    ADD CONSTRAINT memoh_team_key_6e575bdde4a7 UNIQUE (team_id, id);


--
-- Name: bots memoh_team_key_739502fe2ce0; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT memoh_team_key_739502fe2ce0 UNIQUE (team_id, id);


--
-- Name: bot_acl_rules memoh_team_key_785971fcb634; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_acl_rules
    ADD CONSTRAINT memoh_team_key_785971fcb634 UNIQUE (team_id, id);


--
-- Name: bot_email_bindings memoh_team_key_812bc98005b4; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_email_bindings
    ADD CONSTRAINT memoh_team_key_812bc98005b4 UNIQUE (team_id, id);


--
-- Name: bot_heartbeat_logs memoh_team_key_81f61b099ffc; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_heartbeat_logs
    ADD CONSTRAINT memoh_team_key_81f61b099ffc UNIQUE (team_id, id);


--
-- Name: model_variants memoh_team_key_8351313ed607; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_variants
    ADD CONSTRAINT memoh_team_key_8351313ed607 UNIQUE (team_id, id);


--
-- Name: memory_providers memoh_team_key_8babb2fd7a49; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory_providers
    ADD CONSTRAINT memoh_team_key_8babb2fd7a49 UNIQUE (team_id, id);


--
-- Name: bot_storage_bindings memoh_team_key_8c69ca54cae0; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_storage_bindings
    ADD CONSTRAINT memoh_team_key_8c69ca54cae0 UNIQUE (team_id, id);


--
-- Name: schedule_logs memoh_team_key_8dced8a10318; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schedule_logs
    ADD CONSTRAINT memoh_team_key_8dced8a10318 UNIQUE (team_id, id);


--
-- Name: bot_session_events memoh_team_key_9bea1a79597b; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_session_events
    ADD CONSTRAINT memoh_team_key_9bea1a79597b UNIQUE (team_id, id);


--
-- Name: containers memoh_team_key_a179a87cbe03; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.containers
    ADD CONSTRAINT memoh_team_key_a179a87cbe03 UNIQUE (team_id, id);


--
-- Name: schedule memoh_team_key_a305907efebe; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schedule
    ADD CONSTRAINT memoh_team_key_a305907efebe UNIQUE (team_id, id);


--
-- Name: subagent_configs memoh_team_key_a324e2f569ff; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.subagent_configs
    ADD CONSTRAINT memoh_team_key_a324e2f569ff UNIQUE (team_id, session_id);


--
-- Name: bot_sessions memoh_team_key_a39f09c8972a; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_sessions
    ADD CONSTRAINT memoh_team_key_a39f09c8972a UNIQUE (team_id, id);


--
-- Name: media_assets memoh_team_key_a3b5e7229cdf; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_assets
    ADD CONSTRAINT memoh_team_key_a3b5e7229cdf UNIQUE (team_id, id);


--
-- Name: bot_user_grants memoh_team_key_a90f63175197; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_user_grants
    ADD CONSTRAINT memoh_team_key_a90f63175197 UNIQUE (team_id, id);


--
-- Name: bot_channel_routes memoh_team_key_b01c434242c4; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_routes
    ADD CONSTRAINT memoh_team_key_b01c434242c4 UNIQUE (team_id, id);


--
-- Name: bot_channel_configs memoh_team_key_b070e46de89b; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_configs
    ADD CONSTRAINT memoh_team_key_b070e46de89b UNIQUE (team_id, id);


--
-- Name: provider_oauth_tokens memoh_team_key_b1d8ebe8cf22; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_oauth_tokens
    ADD CONSTRAINT memoh_team_key_b1d8ebe8cf22 UNIQUE (team_id, id);


--
-- Name: tool_approval_requests memoh_team_key_b54d8d1ccdec; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_approval_requests
    ADD CONSTRAINT memoh_team_key_b54d8d1ccdec UNIQUE (team_id, id);


--
-- Name: email_outbox memoh_team_key_b6fd1bd87341; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_outbox
    ADD CONSTRAINT memoh_team_key_b6fd1bd87341 UNIQUE (team_id, id);


--
-- Name: tasks memoh_team_key_ba45c4a36084; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tasks
    ADD CONSTRAINT memoh_team_key_ba45c4a36084 UNIQUE (team_id, id);


--
-- Name: bot_channel_admins memoh_team_key_bf4598b3f5ec; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_admins
    ADD CONSTRAINT memoh_team_key_bf4598b3f5ec UNIQUE (team_id, id);


--
-- Name: user_channel_bindings memoh_team_key_c58e4d82cd9a; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_channel_bindings
    ADD CONSTRAINT memoh_team_key_c58e4d82cd9a UNIQUE (team_id, id);


--
-- Name: bot_history_message_assets memoh_team_key_c7ffe2b0b93c; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_message_assets
    ADD CONSTRAINT memoh_team_key_c7ffe2b0b93c UNIQUE (team_id, id);


--
-- Name: bot_history_message_compacts memoh_team_key_dbb8be153799; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_message_compacts
    ADD CONSTRAINT memoh_team_key_dbb8be153799 UNIQUE (team_id, id);


--
-- Name: tts_providers memoh_team_key_dd046f675949; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tts_providers
    ADD CONSTRAINT memoh_team_key_dd046f675949 UNIQUE (team_id, id);


--
-- Name: channel_link_codes memoh_team_key_e76a99926790; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_link_codes
    ADD CONSTRAINT memoh_team_key_e76a99926790 UNIQUE (team_id, token);


--
-- Name: user_runtimes memoh_team_key_eeb71d700aa6; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_runtimes
    ADD CONSTRAINT memoh_team_key_eeb71d700aa6 UNIQUE (team_id, id);


--
-- Name: channel_identities memoh_team_key_f6a93be346d8; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_identities
    ADD CONSTRAINT memoh_team_key_f6a93be346d8 UNIQUE (team_id, id);


--
-- Name: bot_remote_runtime_bindings memoh_team_key_fc924c120c12; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_remote_runtime_bindings
    ADD CONSTRAINT memoh_team_key_fc924c120c12 UNIQUE (team_id, id);


--
-- Name: memory_edges memory_edges_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory_edges
    ADD CONSTRAINT memory_edges_pkey PRIMARY KEY (id);


--
-- Name: memory_edges memory_edges_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory_edges
    ADD CONSTRAINT memory_edges_unique UNIQUE (team_id, bot_id, src_node, dst_node, rel);


--
-- Name: memory_nodes memory_nodes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory_nodes
    ADD CONSTRAINT memory_nodes_pkey PRIMARY KEY (id);


--
-- Name: memory_providers memory_providers_name_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory_providers
    ADD CONSTRAINT memory_providers_name_unique UNIQUE (team_id, name);


--
-- Name: memory_providers memory_providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory_providers
    ADD CONSTRAINT memory_providers_pkey PRIMARY KEY (id);


--
-- Name: bot_history_message_assets message_asset_content_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_message_assets
    ADD CONSTRAINT message_asset_content_unique UNIQUE (team_id, message_id, content_hash);


--
-- Name: model_variants model_variants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_variants
    ADD CONSTRAINT model_variants_pkey PRIMARY KEY (id);


--
-- Name: models models_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.models
    ADD CONSTRAINT models_pkey PRIMARY KEY (id);


--
-- Name: models models_provider_id_model_id_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.models
    ADD CONSTRAINT models_provider_id_model_id_unique UNIQUE (team_id, provider_id, model_id);


--
-- Name: provider_oauth_tokens provider_oauth_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_oauth_tokens
    ADD CONSTRAINT provider_oauth_tokens_pkey PRIMARY KEY (id);


--
-- Name: provider_oauth_tokens provider_oauth_tokens_provider_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_oauth_tokens
    ADD CONSTRAINT provider_oauth_tokens_provider_id_key UNIQUE (team_id, provider_id);


--
-- Name: providers providers_name_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.providers
    ADD CONSTRAINT providers_name_unique UNIQUE (team_id, name);


--
-- Name: providers providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.providers
    ADD CONSTRAINT providers_pkey PRIMARY KEY (id);


--
-- Name: schedule_logs schedule_logs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schedule_logs
    ADD CONSTRAINT schedule_logs_pkey PRIMARY KEY (id);


--
-- Name: schedule schedule_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schedule
    ADD CONSTRAINT schedule_pkey PRIMARY KEY (id);


--
-- Name: schema_migrations schema_migrations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_migrations
    ADD CONSTRAINT schema_migrations_pkey PRIMARY KEY (version);


--
-- Name: search_providers search_providers_name_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_providers
    ADD CONSTRAINT search_providers_name_unique UNIQUE (team_id, name);


--
-- Name: search_providers search_providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_providers
    ADD CONSTRAINT search_providers_pkey PRIMARY KEY (id);


--
-- Name: search_providers search_providers_provider_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_providers
    ADD CONSTRAINT search_providers_provider_unique UNIQUE (team_id, provider);


--
-- Name: snapshots snapshots_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.snapshots
    ADD CONSTRAINT snapshots_pkey PRIMARY KEY (id);


--
-- Name: storage_providers storage_providers_name_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_providers
    ADD CONSTRAINT storage_providers_name_unique UNIQUE (team_id, name);


--
-- Name: storage_providers storage_providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_providers
    ADD CONSTRAINT storage_providers_pkey PRIMARY KEY (id);


--
-- Name: subagent_configs subagent_configs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.subagent_configs
    ADD CONSTRAINT subagent_configs_pkey PRIMARY KEY (session_id);


--
-- Name: subagent_configs subagent_configs_team_session_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.subagent_configs
    ADD CONSTRAINT subagent_configs_team_session_key UNIQUE (team_id, session_id);


--
-- Name: tasks tasks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tasks
    ADD CONSTRAINT tasks_pkey PRIMARY KEY (id);


--
-- Name: team_members team_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_members
    ADD CONSTRAINT team_members_pkey PRIMARY KEY (team_id, user_id);


--
-- Name: teams teams_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.teams
    ADD CONSTRAINT teams_pkey PRIMARY KEY (id);


--
-- Name: tool_approval_requests tool_approval_requests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_approval_requests
    ADD CONSTRAINT tool_approval_requests_pkey PRIMARY KEY (id);


--
-- Name: tool_approval_requests tool_approval_short_id_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_approval_requests
    ADD CONSTRAINT tool_approval_short_id_unique UNIQUE (team_id, session_id, short_id);


--
-- Name: tool_approval_requests tool_approval_tool_call_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_approval_requests
    ADD CONSTRAINT tool_approval_tool_call_unique UNIQUE (team_id, session_id, tool_call_id);


--
-- Name: tts_models tts_models_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tts_models
    ADD CONSTRAINT tts_models_pkey PRIMARY KEY (id);


--
-- Name: tts_providers tts_providers_name_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tts_providers
    ADD CONSTRAINT tts_providers_name_unique UNIQUE (team_id, name);


--
-- Name: tts_providers tts_providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tts_providers
    ADD CONSTRAINT tts_providers_pkey PRIMARY KEY (id);


--
-- Name: user_channel_bindings user_channel_bindings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_channel_bindings
    ADD CONSTRAINT user_channel_bindings_pkey PRIMARY KEY (id);


--
-- Name: user_channel_bindings user_channel_bindings_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_channel_bindings
    ADD CONSTRAINT user_channel_bindings_unique UNIQUE (team_id, user_id, channel_type);


--
-- Name: user_channel_identity_bindings user_channel_identity_bindings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_channel_identity_bindings
    ADD CONSTRAINT user_channel_identity_bindings_pkey PRIMARY KEY (id);


--
-- Name: user_channel_identity_bindings user_channel_identity_bindings_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_channel_identity_bindings
    ADD CONSTRAINT user_channel_identity_bindings_unique UNIQUE (team_id, user_id, channel_identity_id);


--
-- Name: user_input_requests user_input_requests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_input_requests
    ADD CONSTRAINT user_input_requests_pkey PRIMARY KEY (id);


--
-- Name: user_input_requests user_input_short_id_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_input_requests
    ADD CONSTRAINT user_input_short_id_unique UNIQUE (team_id, session_id, short_id);


--
-- Name: user_input_requests user_input_tool_call_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_input_requests
    ADD CONSTRAINT user_input_tool_call_unique UNIQUE (team_id, session_id, tool_call_id);


--
-- Name: user_provider_oauth_tokens user_provider_oauth_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_provider_oauth_tokens
    ADD CONSTRAINT user_provider_oauth_tokens_pkey PRIMARY KEY (id);


--
-- Name: user_provider_oauth_tokens user_provider_oauth_tokens_provider_user_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_provider_oauth_tokens
    ADD CONSTRAINT user_provider_oauth_tokens_provider_user_unique UNIQUE (team_id, provider_id, user_id);


--
-- Name: user_runtimes user_runtimes_api_token_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_runtimes
    ADD CONSTRAINT user_runtimes_api_token_key UNIQUE (team_id, api_token);


--
-- Name: user_runtimes user_runtimes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_runtimes
    ADD CONSTRAINT user_runtimes_pkey PRIMARY KEY (id);


--
-- Name: users users_email_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_email_unique UNIQUE (email);


--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);


--
-- Name: users users_username_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_username_unique UNIQUE (username);


--
-- Name: provider_template_models provider_template_models_identity_unique; Type: CONSTRAINT; Schema: template; Owner: -
--

ALTER TABLE ONLY template.provider_template_models
    ADD CONSTRAINT provider_template_models_identity_unique UNIQUE (provider_template_id, type, model_id);


--
-- Name: provider_template_models provider_template_models_pkey; Type: CONSTRAINT; Schema: template; Owner: -
--

ALTER TABLE ONLY template.provider_template_models
    ADD CONSTRAINT provider_template_models_pkey PRIMARY KEY (id);


--
-- Name: provider_templates provider_templates_domain_key_unique; Type: CONSTRAINT; Schema: template; Owner: -
--

ALTER TABLE ONLY template.provider_templates
    ADD CONSTRAINT provider_templates_domain_key_unique UNIQUE (domain, key);


--
-- Name: provider_templates provider_templates_pkey; Type: CONSTRAINT; Schema: template; Owner: -
--

ALTER TABLE ONLY template.provider_templates
    ADD CONSTRAINT provider_templates_pkey PRIMARY KEY (id);


--
-- Name: idx_bot_acl_rules_bot_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_acl_rules_bot_id ON public.bot_acl_rules USING btree (team_id, bot_id);


--
-- Name: idx_bot_acl_rules_channel_identity_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_acl_rules_channel_identity_id ON public.bot_acl_rules USING btree (team_id, channel_identity_id);


--
-- Name: idx_bot_channel_admins_bot_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_channel_admins_bot_id ON public.bot_channel_admins USING btree (team_id, bot_id);


--
-- Name: idx_bot_channel_admins_channel_identity_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_channel_admins_channel_identity_id ON public.bot_channel_admins USING btree (team_id, channel_identity_id);


--
-- Name: idx_bot_channel_bot_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_channel_bot_id ON public.bot_channel_configs USING btree (team_id, bot_id);


--
-- Name: idx_bot_channel_external_identity; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_bot_channel_external_identity ON public.bot_channel_configs USING btree (team_id, channel_type, external_identity);


--
-- Name: idx_bot_channel_routes_bot; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_channel_routes_bot ON public.bot_channel_routes USING btree (team_id, bot_id);


--
-- Name: idx_bot_channel_routes_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_bot_channel_routes_unique ON public.bot_channel_routes USING btree (team_id, bot_id, channel_type, external_conversation_id, COALESCE(external_thread_id, ''::text));


--
-- Name: idx_bot_email_bindings_bot_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_email_bindings_bot_id ON public.bot_email_bindings USING btree (team_id, bot_id);


--
-- Name: idx_bot_email_bindings_provider_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_email_bindings_provider_id ON public.bot_email_bindings USING btree (team_id, email_provider_id);


--
-- Name: idx_bot_history_messages_bot_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_history_messages_bot_created ON public.bot_history_messages USING btree (team_id, bot_id, created_at);


--
-- Name: idx_bot_history_messages_compact; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_history_messages_compact ON public.bot_history_messages USING btree (team_id, compact_id);


--
-- Name: idx_bot_history_messages_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_history_messages_session ON public.bot_history_messages USING btree (team_id, session_id, created_at);


--
-- Name: idx_bot_history_messages_session_reply; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_history_messages_session_reply ON public.bot_history_messages USING btree (team_id, session_id, source_reply_to_message_id);


--
-- Name: idx_bot_history_messages_session_role_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_history_messages_session_role_created ON public.bot_history_messages USING btree (team_id, session_id, role, created_at, id);


--
-- Name: idx_bot_history_messages_session_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_history_messages_session_source ON public.bot_history_messages USING btree (team_id, session_id, source_message_id);


--
-- Name: idx_bot_history_messages_subagent_fork_context; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_history_messages_subagent_fork_context ON public.bot_history_messages USING btree (team_id, session_id, turn_position, turn_message_seq) WHERE ((turn_visible = false) AND (session_mode = 'subagent'::text) AND ((metadata ->> 'context_scope'::text) = 'subagent_fork'::text));


--
-- Name: idx_bot_history_messages_turn; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_history_messages_turn ON public.bot_history_messages USING btree (team_id, turn_id, turn_message_seq, created_at, id) WHERE (turn_id IS NOT NULL);


--
-- Name: idx_bot_history_messages_turn_seq_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_bot_history_messages_turn_seq_unique ON public.bot_history_messages USING btree (team_id, turn_id, turn_message_seq) WHERE ((turn_id IS NOT NULL) AND (turn_message_seq IS NOT NULL));


--
-- Name: idx_bot_history_messages_visible_session_order; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_history_messages_visible_session_order ON public.bot_history_messages USING btree (team_id, session_id, turn_position DESC, turn_message_seq DESC, created_at DESC, id DESC) WHERE ((turn_visible = true) AND (turn_id IS NOT NULL) AND (turn_position IS NOT NULL) AND (turn_message_seq IS NOT NULL));


--
-- Name: idx_bot_history_messages_visible_session_source_order; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_history_messages_visible_session_source_order ON public.bot_history_messages USING btree (team_id, session_id, source_message_id, turn_position DESC, turn_message_seq DESC, created_at DESC, id DESC) WHERE ((turn_visible = true) AND (source_message_id IS NOT NULL) AND (turn_id IS NOT NULL) AND (turn_position IS NOT NULL) AND (turn_message_seq IS NOT NULL));


--
-- Name: idx_bot_plugin_installations_bot_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_plugin_installations_bot_id ON public.bot_plugin_installations USING btree (team_id, bot_id);


--
-- Name: idx_bot_plugin_installations_plugin_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_plugin_installations_plugin_id ON public.bot_plugin_installations USING btree (team_id, plugin_id);


--
-- Name: idx_bot_plugin_resources_installation_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_plugin_resources_installation_id ON public.bot_plugin_resources USING btree (team_id, installation_id);


--
-- Name: idx_bot_plugin_resources_resource; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_plugin_resources_resource ON public.bot_plugin_resources USING btree (team_id, resource_type, resource_id);


--
-- Name: idx_bot_remote_runtime_bindings_bot_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_remote_runtime_bindings_bot_id ON public.bot_remote_runtime_bindings USING btree (team_id, bot_id);


--
-- Name: idx_bot_remote_runtime_bindings_primary; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_bot_remote_runtime_bindings_primary ON public.bot_remote_runtime_bindings USING btree (bot_id) WHERE (is_primary = true);


--
-- Name: idx_bot_remote_runtime_bindings_runtime_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_remote_runtime_bindings_runtime_id ON public.bot_remote_runtime_bindings USING btree (team_id, runtime_id);


--
-- Name: idx_bot_session_discuss_cursors_route; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_session_discuss_cursors_route ON public.bot_session_discuss_cursors USING btree (team_id, route_id) WHERE (route_id IS NOT NULL);


--
-- Name: idx_bot_sessions_bot_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_sessions_bot_active ON public.bot_sessions USING btree (team_id, bot_id, deleted_at);


--
-- Name: idx_bot_sessions_bot_active_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_sessions_bot_active_updated ON public.bot_sessions USING btree (team_id, bot_id, updated_at DESC, id DESC) WHERE (deleted_at IS NULL);


--
-- Name: idx_bot_sessions_bot_created_by; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_sessions_bot_created_by ON public.bot_sessions USING btree (team_id, bot_id, created_by_user_id, deleted_at);


--
-- Name: idx_bot_sessions_bot_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_sessions_bot_id ON public.bot_sessions USING btree (team_id, bot_id);


--
-- Name: idx_bot_sessions_bot_mode_runtime_active_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_sessions_bot_mode_runtime_active_updated ON public.bot_sessions USING btree (team_id, bot_id, session_mode, runtime_type, updated_at DESC, id DESC) WHERE (deleted_at IS NULL);


--
-- Name: idx_bot_sessions_created_by_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_sessions_created_by_user_id ON public.bot_sessions USING btree (team_id, created_by_user_id) WHERE (created_by_user_id IS NOT NULL);


--
-- Name: idx_bot_sessions_parent; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_sessions_parent ON public.bot_sessions USING btree (team_id, parent_session_id) WHERE (parent_session_id IS NOT NULL);


--
-- Name: idx_bot_sessions_route_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_sessions_route_id ON public.bot_sessions USING btree (team_id, route_id);


--
-- Name: idx_bot_storage_bindings_bot_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_storage_bindings_bot_id ON public.bot_storage_bindings USING btree (team_id, bot_id);


--
-- Name: idx_bot_user_grants_bot_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_user_grants_bot_id ON public.bot_user_grants USING btree (team_id, bot_id);


--
-- Name: idx_bot_user_grants_unique_everyone; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_bot_user_grants_unique_everyone ON public.bot_user_grants USING btree (team_id, bot_id) WHERE (subject_type = 'everyone'::text);


--
-- Name: idx_bot_user_grants_unique_user; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_bot_user_grants_unique_user ON public.bot_user_grants USING btree (team_id, bot_id, user_id) WHERE (subject_type = 'user'::text);


--
-- Name: idx_bot_user_grants_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bot_user_grants_user_id ON public.bot_user_grants USING btree (team_id, user_id);


--
-- Name: idx_bots_name; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_bots_name ON public.bots USING btree (team_id, name);


--
-- Name: idx_bots_owner_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bots_owner_user_id ON public.bots USING btree (team_id, owner_user_id);


--
-- Name: idx_channel_link_codes_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_channel_link_codes_user_id ON public.channel_link_codes USING btree (team_id, user_id);


--
-- Name: idx_compacts_active_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_compacts_active_session ON public.bot_history_message_compacts USING btree (team_id, session_id, anchor_start_ms, started_at) WHERE ((status = 'ok'::text) AND (superseded_at IS NULL));


--
-- Name: idx_compacts_bot_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_compacts_bot_session ON public.bot_history_message_compacts USING btree (team_id, bot_id, session_id, started_at DESC);


--
-- Name: idx_compacts_owner_epoch; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_compacts_owner_epoch ON public.bot_history_message_compacts USING btree (team_id, bot_id, session_id, compaction_epoch, started_at DESC);


--
-- Name: idx_container_versions_container_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_container_versions_container_id ON public.container_versions USING btree (team_id, container_id);


--
-- Name: idx_container_versions_snapshot_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_container_versions_snapshot_id ON public.container_versions USING btree (team_id, snapshot_id);


--
-- Name: idx_containers_bot_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_containers_bot_id ON public.containers USING btree (team_id, bot_id);


--
-- Name: idx_email_oauth_tokens_state; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_oauth_tokens_state ON public.email_oauth_tokens USING btree (team_id, state) WHERE (state <> ''::text);


--
-- Name: idx_email_outbox_bot_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_outbox_bot_id ON public.email_outbox USING btree (team_id, bot_id, created_at DESC);


--
-- Name: idx_email_outbox_provider_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_outbox_provider_id ON public.email_outbox USING btree (team_id, provider_id);


--
-- Name: idx_email_providers_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_providers_user_id ON public.email_providers USING btree (team_id, user_id);


--
-- Name: idx_heartbeat_logs_bot_started; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_heartbeat_logs_bot_started ON public.bot_heartbeat_logs USING btree (team_id, bot_id, started_at DESC);


--
-- Name: idx_lifecycle_events_container_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_lifecycle_events_container_id ON public.lifecycle_events USING btree (team_id, container_id);


--
-- Name: idx_lifecycle_events_event_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_lifecycle_events_event_type ON public.lifecycle_events USING btree (team_id, event_type);


--
-- Name: idx_mcp_connections_bot_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mcp_connections_bot_id ON public.mcp_connections USING btree (team_id, bot_id);


--
-- Name: idx_mcp_connections_plugin_installation_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mcp_connections_plugin_installation_id ON public.mcp_connections USING btree (team_id, managed_by_plugin_installation_id);


--
-- Name: idx_mcp_oauth_tokens_connection_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mcp_oauth_tokens_connection_id ON public.mcp_oauth_tokens USING btree (team_id, connection_id);


--
-- Name: idx_media_assets_bot_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_assets_bot_id ON public.media_assets USING btree (team_id, bot_id);


--
-- Name: idx_media_assets_content_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_assets_content_hash ON public.media_assets USING btree (team_id, content_hash);


--
-- Name: idx_memory_edges_dst; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_memory_edges_dst ON public.memory_edges USING btree (team_id, bot_id, dst_node);


--
-- Name: idx_memory_edges_rel; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_memory_edges_rel ON public.memory_edges USING btree (team_id, bot_id, rel);


--
-- Name: idx_memory_edges_src; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_memory_edges_src ON public.memory_edges USING btree (team_id, bot_id, src_node);


--
-- Name: idx_memory_nodes_bot_layer; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_memory_nodes_bot_layer ON public.memory_nodes USING btree (team_id, bot_id, layer);


--
-- Name: idx_memory_nodes_bot_prof; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_memory_nodes_bot_prof ON public.memory_nodes USING btree (team_id, bot_id, profile_ref);


--
-- Name: idx_memory_nodes_bot_topic; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_memory_nodes_bot_topic ON public.memory_nodes USING btree (team_id, bot_id, topic);


--
-- Name: idx_memory_nodes_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_memory_nodes_updated ON public.memory_nodes USING btree (team_id, bot_id, updated_at DESC);


--
-- Name: idx_message_assets_message_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_message_assets_message_id ON public.bot_history_message_assets USING btree (team_id, message_id);


--
-- Name: idx_model_variants_model_uuid; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_variants_model_uuid ON public.model_variants USING btree (team_id, model_uuid);


--
-- Name: idx_model_variants_variant_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_variants_variant_id ON public.model_variants USING btree (team_id, variant_id);


--
-- Name: idx_provider_oauth_tokens_state; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_provider_oauth_tokens_state ON public.provider_oauth_tokens USING btree (team_id, state) WHERE (state <> ''::text);


--
-- Name: idx_providers_provider_template_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_providers_provider_template_id ON public.providers USING btree (team_id, provider_template_id);


--
-- Name: idx_schedule_bot_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_schedule_bot_id ON public.schedule USING btree (team_id, bot_id);


--
-- Name: idx_schedule_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_schedule_enabled ON public.schedule USING btree (team_id, enabled);


--
-- Name: idx_schedule_logs_bot; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_schedule_logs_bot ON public.schedule_logs USING btree (team_id, bot_id, started_at DESC);


--
-- Name: idx_schedule_logs_schedule; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_schedule_logs_schedule ON public.schedule_logs USING btree (team_id, schedule_id, started_at DESC);


--
-- Name: idx_session_events_dedup; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_session_events_dedup ON public.bot_session_events USING btree (team_id, session_id, event_kind, external_message_id) WHERE ((external_message_id IS NOT NULL) AND (external_message_id <> ''::text));


--
-- Name: idx_session_events_session_received; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_events_session_received ON public.bot_session_events USING btree (team_id, session_id, received_at_ms);


--
-- Name: idx_snapshots_container_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_snapshots_container_created_at ON public.snapshots USING btree (team_id, container_id, created_at DESC);


--
-- Name: idx_snapshots_container_runtime_name; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_snapshots_container_runtime_name ON public.snapshots USING btree (team_id, container_id, runtime_snapshot_name);


--
-- Name: idx_snapshots_runtime_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_snapshots_runtime_name ON public.snapshots USING btree (team_id, runtime_snapshot_name);


--
-- Name: idx_subagent_configs_team_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_subagent_configs_team_model ON public.subagent_configs USING btree (team_id, model_uuid);


--
-- Name: idx_tasks_exec_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tasks_exec_id ON public.tasks USING btree (team_id, exec_id);


--
-- Name: idx_tasks_pid; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tasks_pid ON public.tasks USING btree (team_id, pid);


--
-- Name: idx_tool_approval_bot_status_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_approval_bot_status_created ON public.tool_approval_requests USING btree (team_id, bot_id, status, created_at);


--
-- Name: idx_tool_approval_prompt_external; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_approval_prompt_external ON public.tool_approval_requests USING btree (team_id, prompt_external_message_id) WHERE (prompt_external_message_id <> ''::text);


--
-- Name: idx_tool_approval_session_status_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_approval_session_status_created ON public.tool_approval_requests USING btree (team_id, session_id, status, created_at);


--
-- Name: idx_tts_models_provider_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tts_models_provider_id ON public.tts_models USING btree (team_id, tts_provider_id);


--
-- Name: idx_user_channel_bindings_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_channel_bindings_user_id ON public.user_channel_bindings USING btree (team_id, user_id);


--
-- Name: idx_user_channel_identity_bindings_channel_identity_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_channel_identity_bindings_channel_identity_id ON public.user_channel_identity_bindings USING btree (team_id, channel_identity_id);


--
-- Name: idx_user_channel_identity_bindings_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_channel_identity_bindings_user_id ON public.user_channel_identity_bindings USING btree (team_id, user_id);


--
-- Name: idx_user_input_bot_status_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_input_bot_status_created ON public.user_input_requests USING btree (team_id, bot_id, status, created_at);


--
-- Name: idx_user_input_prompt_external; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_input_prompt_external ON public.user_input_requests USING btree (team_id, prompt_external_message_id) WHERE (prompt_external_message_id <> ''::text);


--
-- Name: idx_user_input_session_status_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_input_session_status_created ON public.user_input_requests USING btree (team_id, session_id, status, created_at);


--
-- Name: idx_user_provider_oauth_tokens_state; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_provider_oauth_tokens_state ON public.user_provider_oauth_tokens USING btree (team_id, state) WHERE (state <> ''::text);


--
-- Name: idx_user_runtimes_active_user_name; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_user_runtimes_active_user_name ON public.user_runtimes USING btree (user_id, lower(name)) WHERE (revoked_at IS NULL);


--
-- Name: idx_user_runtimes_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_runtimes_user_id ON public.user_runtimes USING btree (team_id, user_id);


--
-- Name: team_members_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX team_members_user_id_idx ON public.team_members USING btree (user_id);


--
-- Name: teams_slug_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX teams_slug_unique ON public.teams USING btree (slug) WHERE (slug IS NOT NULL);


--
-- Name: idx_provider_template_models_template_active_order; Type: INDEX; Schema: template; Owner: -
--

CREATE INDEX idx_provider_template_models_template_active_order ON template.provider_template_models USING btree (provider_template_id, active, sort_order, model_id);


--
-- Name: idx_provider_templates_domain_active_order; Type: INDEX; Schema: template; Owner: -
--

CREATE INDEX idx_provider_templates_domain_active_order ON template.provider_templates USING btree (domain, active, sort_order, name);


--
-- Name: team_members team_members_last_active_admin_guard; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER team_members_last_active_admin_guard BEFORE DELETE OR UPDATE OF role, is_active ON public.team_members FOR EACH ROW EXECUTE FUNCTION public.memoh_guard_last_active_team_admin();


--
-- Name: bot_acl_rules bot_acl_rules_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_acl_rules
    ADD CONSTRAINT bot_acl_rules_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_acl_rules bot_acl_rules_channel_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_acl_rules
    ADD CONSTRAINT bot_acl_rules_channel_identity_id_fkey FOREIGN KEY (team_id, channel_identity_id) REFERENCES public.channel_identities(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_acl_rules bot_acl_rules_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_acl_rules
    ADD CONSTRAINT bot_acl_rules_created_by_user_id_fkey FOREIGN KEY (team_id, created_by_user_id) REFERENCES public.team_members(team_id, user_id) ON DELETE SET NULL (created_by_user_id);


--
-- Name: bot_acl_rules bot_acl_rules_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_acl_rules
    ADD CONSTRAINT bot_acl_rules_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_channel_admins bot_channel_admins_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_admins
    ADD CONSTRAINT bot_channel_admins_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_channel_admins bot_channel_admins_channel_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_admins
    ADD CONSTRAINT bot_channel_admins_channel_identity_id_fkey FOREIGN KEY (team_id, channel_identity_id) REFERENCES public.channel_identities(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_channel_admins bot_channel_admins_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_admins
    ADD CONSTRAINT bot_channel_admins_created_by_user_id_fkey FOREIGN KEY (team_id, created_by_user_id) REFERENCES public.team_members(team_id, user_id) ON DELETE SET NULL (created_by_user_id);


--
-- Name: bot_channel_admins bot_channel_admins_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_admins
    ADD CONSTRAINT bot_channel_admins_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_channel_configs bot_channel_configs_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_configs
    ADD CONSTRAINT bot_channel_configs_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_channel_configs bot_channel_configs_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_configs
    ADD CONSTRAINT bot_channel_configs_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_channel_routes bot_channel_routes_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_routes
    ADD CONSTRAINT bot_channel_routes_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_channel_routes bot_channel_routes_channel_config_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_routes
    ADD CONSTRAINT bot_channel_routes_channel_config_id_fkey FOREIGN KEY (team_id, channel_config_id) REFERENCES public.bot_channel_configs(team_id, id) ON DELETE SET NULL (channel_config_id);


--
-- Name: bot_channel_routes bot_channel_routes_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_routes
    ADD CONSTRAINT bot_channel_routes_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_email_bindings bot_email_bindings_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_email_bindings
    ADD CONSTRAINT bot_email_bindings_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_email_bindings bot_email_bindings_email_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_email_bindings
    ADD CONSTRAINT bot_email_bindings_email_provider_id_fkey FOREIGN KEY (team_id, email_provider_id) REFERENCES public.email_providers(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_email_bindings bot_email_bindings_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_email_bindings
    ADD CONSTRAINT bot_email_bindings_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_heartbeat_logs bot_heartbeat_logs_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_heartbeat_logs
    ADD CONSTRAINT bot_heartbeat_logs_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_heartbeat_logs bot_heartbeat_logs_model_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_heartbeat_logs
    ADD CONSTRAINT bot_heartbeat_logs_model_id_fkey FOREIGN KEY (team_id, model_id) REFERENCES public.models(team_id, id) ON DELETE SET NULL (model_id);


--
-- Name: bot_heartbeat_logs bot_heartbeat_logs_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_heartbeat_logs
    ADD CONSTRAINT bot_heartbeat_logs_session_id_fkey FOREIGN KEY (team_id, session_id) REFERENCES public.bot_sessions(team_id, id) ON DELETE SET NULL (session_id);


--
-- Name: bot_heartbeat_logs bot_heartbeat_logs_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_heartbeat_logs
    ADD CONSTRAINT bot_heartbeat_logs_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_history_message_assets bot_history_message_assets_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_message_assets
    ADD CONSTRAINT bot_history_message_assets_message_id_fkey FOREIGN KEY (team_id, message_id) REFERENCES public.bot_history_messages(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_history_message_assets bot_history_message_assets_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_message_assets
    ADD CONSTRAINT bot_history_message_assets_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_history_message_compacts bot_history_message_compacts_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_message_compacts
    ADD CONSTRAINT bot_history_message_compacts_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_history_message_compacts bot_history_message_compacts_model_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_message_compacts
    ADD CONSTRAINT bot_history_message_compacts_model_id_fkey FOREIGN KEY (team_id, model_id) REFERENCES public.models(team_id, id) ON DELETE SET NULL (model_id);


--
-- Name: bot_history_message_compacts bot_history_message_compacts_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_message_compacts
    ADD CONSTRAINT bot_history_message_compacts_session_id_fkey FOREIGN KEY (team_id, session_id) REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_history_message_compacts bot_history_message_compacts_superseded_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_message_compacts
    ADD CONSTRAINT bot_history_message_compacts_superseded_by_fkey FOREIGN KEY (team_id, superseded_by) REFERENCES public.bot_history_message_compacts(team_id, id) ON DELETE SET NULL (superseded_by);


--
-- Name: bot_history_message_compacts bot_history_message_compacts_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_message_compacts
    ADD CONSTRAINT bot_history_message_compacts_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_history_messages bot_history_messages_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_messages
    ADD CONSTRAINT bot_history_messages_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_history_messages bot_history_messages_event_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_messages
    ADD CONSTRAINT bot_history_messages_event_id_fkey FOREIGN KEY (team_id, event_id) REFERENCES public.bot_session_events(team_id, id) ON DELETE SET NULL (event_id);


--
-- Name: bot_history_messages bot_history_messages_model_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_messages
    ADD CONSTRAINT bot_history_messages_model_id_fkey FOREIGN KEY (team_id, model_id) REFERENCES public.models(team_id, id) ON DELETE SET NULL (model_id);


--
-- Name: bot_history_messages bot_history_messages_sender_account_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_messages
    ADD CONSTRAINT bot_history_messages_sender_account_user_id_fkey FOREIGN KEY (team_id, sender_account_user_id) REFERENCES public.team_members(team_id, user_id);


--
-- Name: bot_history_messages bot_history_messages_sender_channel_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_messages
    ADD CONSTRAINT bot_history_messages_sender_channel_identity_id_fkey FOREIGN KEY (team_id, sender_channel_identity_id) REFERENCES public.channel_identities(team_id, id);


--
-- Name: bot_history_messages bot_history_messages_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_messages
    ADD CONSTRAINT bot_history_messages_session_id_fkey FOREIGN KEY (team_id, session_id) REFERENCES public.bot_sessions(team_id, id) ON DELETE SET NULL (session_id);


--
-- Name: bot_history_messages bot_history_messages_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_messages
    ADD CONSTRAINT bot_history_messages_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_plugin_installations bot_plugin_installations_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_plugin_installations
    ADD CONSTRAINT bot_plugin_installations_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_plugin_installations bot_plugin_installations_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_plugin_installations
    ADD CONSTRAINT bot_plugin_installations_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_plugin_resources bot_plugin_resources_installation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_plugin_resources
    ADD CONSTRAINT bot_plugin_resources_installation_id_fkey FOREIGN KEY (team_id, installation_id) REFERENCES public.bot_plugin_installations(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_plugin_resources bot_plugin_resources_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_plugin_resources
    ADD CONSTRAINT bot_plugin_resources_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_remote_runtime_bindings bot_remote_runtime_bindings_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_remote_runtime_bindings
    ADD CONSTRAINT bot_remote_runtime_bindings_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_remote_runtime_bindings bot_remote_runtime_bindings_runtime_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_remote_runtime_bindings
    ADD CONSTRAINT bot_remote_runtime_bindings_runtime_id_fkey FOREIGN KEY (team_id, runtime_id) REFERENCES public.user_runtimes(team_id, id) ON DELETE RESTRICT;


--
-- Name: bot_remote_runtime_bindings bot_remote_runtime_bindings_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_remote_runtime_bindings
    ADD CONSTRAINT bot_remote_runtime_bindings_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_session_discuss_cursors bot_session_discuss_cursors_route_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_session_discuss_cursors
    ADD CONSTRAINT bot_session_discuss_cursors_route_id_fkey FOREIGN KEY (team_id, route_id) REFERENCES public.bot_channel_routes(team_id, id) ON DELETE SET NULL (route_id);


--
-- Name: bot_session_discuss_cursors bot_session_discuss_cursors_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_session_discuss_cursors
    ADD CONSTRAINT bot_session_discuss_cursors_session_id_fkey FOREIGN KEY (team_id, session_id) REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_session_discuss_cursors bot_session_discuss_cursors_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_session_discuss_cursors
    ADD CONSTRAINT bot_session_discuss_cursors_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_session_events bot_session_events_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_session_events
    ADD CONSTRAINT bot_session_events_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_session_events bot_session_events_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_session_events
    ADD CONSTRAINT bot_session_events_session_id_fkey FOREIGN KEY (team_id, session_id) REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_session_events bot_session_events_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_session_events
    ADD CONSTRAINT bot_session_events_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_sessions bot_sessions_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_sessions
    ADD CONSTRAINT bot_sessions_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_sessions bot_sessions_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_sessions
    ADD CONSTRAINT bot_sessions_created_by_user_id_fkey FOREIGN KEY (team_id, created_by_user_id) REFERENCES public.team_members(team_id, user_id) ON DELETE SET NULL (created_by_user_id);


--
-- Name: bot_sessions bot_sessions_parent_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_sessions
    ADD CONSTRAINT bot_sessions_parent_session_id_fkey FOREIGN KEY (team_id, parent_session_id) REFERENCES public.bot_sessions(team_id, id) ON DELETE SET NULL (parent_session_id);


--
-- Name: bot_sessions bot_sessions_route_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_sessions
    ADD CONSTRAINT bot_sessions_route_id_fkey FOREIGN KEY (team_id, route_id) REFERENCES public.bot_channel_routes(team_id, id) ON DELETE SET NULL (route_id);


--
-- Name: bot_sessions bot_sessions_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_sessions
    ADD CONSTRAINT bot_sessions_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_storage_bindings bot_storage_bindings_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_storage_bindings
    ADD CONSTRAINT bot_storage_bindings_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_storage_bindings bot_storage_bindings_storage_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_storage_bindings
    ADD CONSTRAINT bot_storage_bindings_storage_provider_id_fkey FOREIGN KEY (team_id, storage_provider_id) REFERENCES public.storage_providers(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_storage_bindings bot_storage_bindings_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_storage_bindings
    ADD CONSTRAINT bot_storage_bindings_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_user_grants bot_user_grants_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_user_grants
    ADD CONSTRAINT bot_user_grants_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_user_grants bot_user_grants_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_user_grants
    ADD CONSTRAINT bot_user_grants_created_by_user_id_fkey FOREIGN KEY (team_id, created_by_user_id) REFERENCES public.team_members(team_id, user_id) ON DELETE SET NULL (created_by_user_id);


--
-- Name: bot_user_grants bot_user_grants_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_user_grants
    ADD CONSTRAINT bot_user_grants_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_user_grants bot_user_grants_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_user_grants
    ADD CONSTRAINT bot_user_grants_user_id_fkey FOREIGN KEY (team_id, user_id) REFERENCES public.team_members(team_id, user_id) ON DELETE CASCADE;


--
-- Name: bot_workspace_resource_limits bot_workspace_resource_limits_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_workspace_resource_limits
    ADD CONSTRAINT bot_workspace_resource_limits_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: bot_workspace_resource_limits bot_workspace_resource_limits_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_workspace_resource_limits
    ADD CONSTRAINT bot_workspace_resource_limits_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bots bots_chat_model_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_chat_model_id_fkey FOREIGN KEY (team_id, chat_model_id) REFERENCES public.models(team_id, id) ON DELETE SET NULL (chat_model_id);


--
-- Name: bots bots_compaction_model_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_compaction_model_id_fkey FOREIGN KEY (team_id, compaction_model_id) REFERENCES public.models(team_id, id) ON DELETE SET NULL (compaction_model_id);


--
-- Name: bots bots_discuss_probe_model_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_discuss_probe_model_id_fkey FOREIGN KEY (team_id, discuss_probe_model_id) REFERENCES public.models(team_id, id) ON DELETE SET NULL (discuss_probe_model_id);


--
-- Name: bots bots_fetch_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_fetch_provider_id_fkey FOREIGN KEY (team_id, fetch_provider_id) REFERENCES public.fetch_providers(team_id, id) ON DELETE SET NULL (fetch_provider_id);


--
-- Name: bots bots_heartbeat_model_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_heartbeat_model_id_fkey FOREIGN KEY (team_id, heartbeat_model_id) REFERENCES public.models(team_id, id) ON DELETE SET NULL (heartbeat_model_id);


--
-- Name: bots bots_image_model_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_image_model_id_fkey FOREIGN KEY (team_id, image_model_id) REFERENCES public.models(team_id, id) ON DELETE SET NULL (image_model_id);


--
-- Name: bots bots_memory_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_memory_provider_id_fkey FOREIGN KEY (team_id, memory_provider_id) REFERENCES public.memory_providers(team_id, id) ON DELETE SET NULL (memory_provider_id);


--
-- Name: bots bots_owner_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_owner_user_id_fkey FOREIGN KEY (team_id, owner_user_id) REFERENCES public.team_members(team_id, user_id) ON DELETE CASCADE;


--
-- Name: bots bots_search_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_search_provider_id_fkey FOREIGN KEY (team_id, search_provider_id) REFERENCES public.search_providers(team_id, id) ON DELETE SET NULL (search_provider_id);


--
-- Name: bots bots_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bots bots_transcription_model_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_transcription_model_id_fkey FOREIGN KEY (team_id, transcription_model_id) REFERENCES public.models(team_id, id) ON DELETE SET NULL (transcription_model_id);


--
-- Name: bots bots_tts_model_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_tts_model_id_fkey FOREIGN KEY (team_id, tts_model_id) REFERENCES public.models(team_id, id) ON DELETE SET NULL (tts_model_id);


--
-- Name: bots bots_video_model_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_video_model_id_fkey FOREIGN KEY (team_id, video_model_id) REFERENCES public.models(team_id, id) ON DELETE SET NULL (video_model_id);


--
-- Name: channel_identities channel_identities_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_identities
    ADD CONSTRAINT channel_identities_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: channel_link_codes channel_link_codes_consumed_channel_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_link_codes
    ADD CONSTRAINT channel_link_codes_consumed_channel_identity_id_fkey FOREIGN KEY (team_id, consumed_channel_identity_id) REFERENCES public.channel_identities(team_id, id) ON DELETE SET NULL (consumed_channel_identity_id);


--
-- Name: channel_link_codes channel_link_codes_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_link_codes
    ADD CONSTRAINT channel_link_codes_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: channel_link_codes channel_link_codes_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_link_codes
    ADD CONSTRAINT channel_link_codes_user_id_fkey FOREIGN KEY (team_id, user_id) REFERENCES public.team_members(team_id, user_id) ON DELETE CASCADE;


--
-- Name: container_versions container_versions_container_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.container_versions
    ADD CONSTRAINT container_versions_container_id_fkey FOREIGN KEY (team_id, container_id) REFERENCES public.containers(team_id, container_id) ON DELETE CASCADE;


--
-- Name: container_versions container_versions_snapshot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.container_versions
    ADD CONSTRAINT container_versions_snapshot_id_fkey FOREIGN KEY (team_id, snapshot_id) REFERENCES public.snapshots(team_id, id) ON DELETE RESTRICT;


--
-- Name: container_versions container_versions_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.container_versions
    ADD CONSTRAINT container_versions_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: containers containers_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.containers
    ADD CONSTRAINT containers_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: containers containers_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.containers
    ADD CONSTRAINT containers_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: email_oauth_tokens email_oauth_tokens_email_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_oauth_tokens
    ADD CONSTRAINT email_oauth_tokens_email_provider_id_fkey FOREIGN KEY (team_id, email_provider_id) REFERENCES public.email_providers(team_id, id) ON DELETE CASCADE;


--
-- Name: email_oauth_tokens email_oauth_tokens_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_oauth_tokens
    ADD CONSTRAINT email_oauth_tokens_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: email_outbox email_outbox_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_outbox
    ADD CONSTRAINT email_outbox_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: email_outbox email_outbox_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_outbox
    ADD CONSTRAINT email_outbox_provider_id_fkey FOREIGN KEY (team_id, provider_id) REFERENCES public.email_providers(team_id, id) ON DELETE CASCADE;


--
-- Name: email_outbox email_outbox_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_outbox
    ADD CONSTRAINT email_outbox_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: email_providers email_providers_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_providers
    ADD CONSTRAINT email_providers_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: email_providers email_providers_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_providers
    ADD CONSTRAINT email_providers_user_id_fkey FOREIGN KEY (team_id, user_id) REFERENCES public.team_members(team_id, user_id) ON DELETE CASCADE;


--
-- Name: fetch_providers fetch_providers_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fetch_providers
    ADD CONSTRAINT fetch_providers_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: bot_channel_routes fk_bot_channel_routes_active_session; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_channel_routes
    ADD CONSTRAINT fk_bot_channel_routes_active_session FOREIGN KEY (team_id, active_session_id) REFERENCES public.bot_sessions(team_id, id) ON DELETE SET NULL (active_session_id);


--
-- Name: bot_history_messages fk_compact_id; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_history_messages
    ADD CONSTRAINT fk_compact_id FOREIGN KEY (team_id, compact_id) REFERENCES public.bot_history_message_compacts(team_id, id) ON DELETE SET NULL (compact_id);


--
-- Name: lifecycle_events lifecycle_events_container_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.lifecycle_events
    ADD CONSTRAINT lifecycle_events_container_id_fkey FOREIGN KEY (team_id, container_id) REFERENCES public.containers(team_id, container_id) ON DELETE CASCADE;


--
-- Name: lifecycle_events lifecycle_events_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.lifecycle_events
    ADD CONSTRAINT lifecycle_events_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: mcp_connections mcp_connections_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.mcp_connections
    ADD CONSTRAINT mcp_connections_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: mcp_connections mcp_connections_managed_by_plugin_installation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.mcp_connections
    ADD CONSTRAINT mcp_connections_managed_by_plugin_installation_id_fkey FOREIGN KEY (team_id, managed_by_plugin_installation_id) REFERENCES public.bot_plugin_installations(team_id, id) ON DELETE SET NULL (managed_by_plugin_installation_id);


--
-- Name: mcp_connections mcp_connections_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.mcp_connections
    ADD CONSTRAINT mcp_connections_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: mcp_oauth_tokens mcp_oauth_tokens_connection_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.mcp_oauth_tokens
    ADD CONSTRAINT mcp_oauth_tokens_connection_id_fkey FOREIGN KEY (team_id, connection_id) REFERENCES public.mcp_connections(team_id, id) ON DELETE CASCADE;


--
-- Name: mcp_oauth_tokens mcp_oauth_tokens_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.mcp_oauth_tokens
    ADD CONSTRAINT mcp_oauth_tokens_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: media_assets media_assets_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_assets
    ADD CONSTRAINT media_assets_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: media_assets media_assets_storage_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_assets
    ADD CONSTRAINT media_assets_storage_provider_id_fkey FOREIGN KEY (team_id, storage_provider_id) REFERENCES public.storage_providers(team_id, id) ON DELETE SET NULL (storage_provider_id);


--
-- Name: media_assets media_assets_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_assets
    ADD CONSTRAINT media_assets_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: memory_edges memory_edges_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory_edges
    ADD CONSTRAINT memory_edges_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: memory_edges memory_edges_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory_edges
    ADD CONSTRAINT memory_edges_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: memory_nodes memory_nodes_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory_nodes
    ADD CONSTRAINT memory_nodes_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: memory_nodes memory_nodes_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory_nodes
    ADD CONSTRAINT memory_nodes_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: memory_providers memory_providers_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory_providers
    ADD CONSTRAINT memory_providers_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: model_variants model_variants_model_uuid_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_variants
    ADD CONSTRAINT model_variants_model_uuid_fkey FOREIGN KEY (team_id, model_uuid) REFERENCES public.models(team_id, id) ON DELETE CASCADE;


--
-- Name: model_variants model_variants_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_variants
    ADD CONSTRAINT model_variants_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: models models_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.models
    ADD CONSTRAINT models_provider_id_fkey FOREIGN KEY (team_id, provider_id) REFERENCES public.providers(team_id, id) ON DELETE CASCADE;


--
-- Name: models models_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.models
    ADD CONSTRAINT models_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: provider_oauth_tokens provider_oauth_tokens_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_oauth_tokens
    ADD CONSTRAINT provider_oauth_tokens_provider_id_fkey FOREIGN KEY (team_id, provider_id) REFERENCES public.providers(team_id, id) ON DELETE CASCADE;


--
-- Name: provider_oauth_tokens provider_oauth_tokens_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_oauth_tokens
    ADD CONSTRAINT provider_oauth_tokens_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: providers providers_provider_template_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.providers
    ADD CONSTRAINT providers_provider_template_id_fkey FOREIGN KEY (provider_template_id) REFERENCES template.provider_templates(id) ON DELETE SET NULL (provider_template_id);


--
-- Name: providers providers_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.providers
    ADD CONSTRAINT providers_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: schedule schedule_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schedule
    ADD CONSTRAINT schedule_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: schedule_logs schedule_logs_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schedule_logs
    ADD CONSTRAINT schedule_logs_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: schedule_logs schedule_logs_model_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schedule_logs
    ADD CONSTRAINT schedule_logs_model_id_fkey FOREIGN KEY (team_id, model_id) REFERENCES public.models(team_id, id) ON DELETE SET NULL (model_id);


--
-- Name: schedule_logs schedule_logs_schedule_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schedule_logs
    ADD CONSTRAINT schedule_logs_schedule_id_fkey FOREIGN KEY (team_id, schedule_id) REFERENCES public.schedule(team_id, id) ON DELETE CASCADE;


--
-- Name: schedule_logs schedule_logs_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schedule_logs
    ADD CONSTRAINT schedule_logs_session_id_fkey FOREIGN KEY (team_id, session_id) REFERENCES public.bot_sessions(team_id, id) ON DELETE SET NULL (session_id);


--
-- Name: schedule_logs schedule_logs_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schedule_logs
    ADD CONSTRAINT schedule_logs_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: schedule schedule_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schedule
    ADD CONSTRAINT schedule_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: search_providers search_providers_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_providers
    ADD CONSTRAINT search_providers_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: snapshots snapshots_container_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.snapshots
    ADD CONSTRAINT snapshots_container_id_fkey FOREIGN KEY (team_id, container_id) REFERENCES public.containers(team_id, container_id) ON DELETE CASCADE;


--
-- Name: snapshots snapshots_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.snapshots
    ADD CONSTRAINT snapshots_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: storage_providers storage_providers_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_providers
    ADD CONSTRAINT storage_providers_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: subagent_configs subagent_configs_model_uuid_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.subagent_configs
    ADD CONSTRAINT subagent_configs_model_uuid_fkey FOREIGN KEY (team_id, model_uuid) REFERENCES public.models(team_id, id) ON DELETE SET NULL (model_uuid);


--
-- Name: subagent_configs subagent_configs_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.subagent_configs
    ADD CONSTRAINT subagent_configs_session_id_fkey FOREIGN KEY (team_id, session_id) REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE;


--
-- Name: subagent_configs subagent_configs_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.subagent_configs
    ADD CONSTRAINT subagent_configs_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: tasks tasks_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tasks
    ADD CONSTRAINT tasks_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: team_members team_members_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_members
    ADD CONSTRAINT team_members_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: team_members team_members_title_model_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_members
    ADD CONSTRAINT team_members_title_model_id_fkey FOREIGN KEY (team_id, title_model_id) REFERENCES public.models(team_id, id) ON DELETE SET NULL (title_model_id);


--
-- Name: team_members team_members_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_members
    ADD CONSTRAINT team_members_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: tool_approval_requests tool_approval_requests_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_approval_requests
    ADD CONSTRAINT tool_approval_requests_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: tool_approval_requests tool_approval_requests_channel_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_approval_requests
    ADD CONSTRAINT tool_approval_requests_channel_identity_id_fkey FOREIGN KEY (team_id, channel_identity_id) REFERENCES public.channel_identities(team_id, id) ON DELETE SET NULL (channel_identity_id);


--
-- Name: tool_approval_requests tool_approval_requests_decided_by_channel_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_approval_requests
    ADD CONSTRAINT tool_approval_requests_decided_by_channel_identity_id_fkey FOREIGN KEY (team_id, decided_by_channel_identity_id) REFERENCES public.channel_identities(team_id, id) ON DELETE SET NULL (decided_by_channel_identity_id);


--
-- Name: tool_approval_requests tool_approval_requests_prompt_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_approval_requests
    ADD CONSTRAINT tool_approval_requests_prompt_message_id_fkey FOREIGN KEY (team_id, prompt_message_id) REFERENCES public.bot_history_messages(team_id, id) ON DELETE SET NULL (prompt_message_id);


--
-- Name: tool_approval_requests tool_approval_requests_requested_by_channel_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_approval_requests
    ADD CONSTRAINT tool_approval_requests_requested_by_channel_identity_id_fkey FOREIGN KEY (team_id, requested_by_channel_identity_id) REFERENCES public.channel_identities(team_id, id) ON DELETE SET NULL (requested_by_channel_identity_id);


--
-- Name: tool_approval_requests tool_approval_requests_requested_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_approval_requests
    ADD CONSTRAINT tool_approval_requests_requested_message_id_fkey FOREIGN KEY (team_id, requested_message_id) REFERENCES public.bot_history_messages(team_id, id) ON DELETE SET NULL (requested_message_id);


--
-- Name: tool_approval_requests tool_approval_requests_route_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_approval_requests
    ADD CONSTRAINT tool_approval_requests_route_id_fkey FOREIGN KEY (team_id, route_id) REFERENCES public.bot_channel_routes(team_id, id) ON DELETE SET NULL (route_id);


--
-- Name: tool_approval_requests tool_approval_requests_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_approval_requests
    ADD CONSTRAINT tool_approval_requests_session_id_fkey FOREIGN KEY (team_id, session_id) REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE;


--
-- Name: tool_approval_requests tool_approval_requests_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_approval_requests
    ADD CONSTRAINT tool_approval_requests_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: tts_models tts_models_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tts_models
    ADD CONSTRAINT tts_models_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: tts_models tts_models_tts_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tts_models
    ADD CONSTRAINT tts_models_tts_provider_id_fkey FOREIGN KEY (team_id, tts_provider_id) REFERENCES public.tts_providers(team_id, id) ON DELETE CASCADE;


--
-- Name: tts_providers tts_providers_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tts_providers
    ADD CONSTRAINT tts_providers_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: user_channel_bindings user_channel_bindings_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_channel_bindings
    ADD CONSTRAINT user_channel_bindings_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: user_channel_bindings user_channel_bindings_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_channel_bindings
    ADD CONSTRAINT user_channel_bindings_user_id_fkey FOREIGN KEY (team_id, user_id) REFERENCES public.team_members(team_id, user_id) ON DELETE CASCADE;


--
-- Name: user_channel_identity_bindings user_channel_identity_bindings_channel_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_channel_identity_bindings
    ADD CONSTRAINT user_channel_identity_bindings_channel_identity_id_fkey FOREIGN KEY (team_id, channel_identity_id) REFERENCES public.channel_identities(team_id, id) ON DELETE CASCADE;


--
-- Name: user_channel_identity_bindings user_channel_identity_bindings_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_channel_identity_bindings
    ADD CONSTRAINT user_channel_identity_bindings_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: user_channel_identity_bindings user_channel_identity_bindings_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_channel_identity_bindings
    ADD CONSTRAINT user_channel_identity_bindings_user_id_fkey FOREIGN KEY (team_id, user_id) REFERENCES public.team_members(team_id, user_id) ON DELETE CASCADE;


--
-- Name: user_input_requests user_input_requests_assistant_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_input_requests
    ADD CONSTRAINT user_input_requests_assistant_message_id_fkey FOREIGN KEY (team_id, assistant_message_id) REFERENCES public.bot_history_messages(team_id, id) ON DELETE SET NULL (assistant_message_id);


--
-- Name: user_input_requests user_input_requests_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_input_requests
    ADD CONSTRAINT user_input_requests_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE;


--
-- Name: user_input_requests user_input_requests_channel_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_input_requests
    ADD CONSTRAINT user_input_requests_channel_identity_id_fkey FOREIGN KEY (team_id, channel_identity_id) REFERENCES public.channel_identities(team_id, id) ON DELETE SET NULL (channel_identity_id);


--
-- Name: user_input_requests user_input_requests_prompt_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_input_requests
    ADD CONSTRAINT user_input_requests_prompt_message_id_fkey FOREIGN KEY (team_id, prompt_message_id) REFERENCES public.bot_history_messages(team_id, id) ON DELETE SET NULL (prompt_message_id);


--
-- Name: user_input_requests user_input_requests_requested_by_channel_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_input_requests
    ADD CONSTRAINT user_input_requests_requested_by_channel_identity_id_fkey FOREIGN KEY (team_id, requested_by_channel_identity_id) REFERENCES public.channel_identities(team_id, id) ON DELETE SET NULL (requested_by_channel_identity_id);


--
-- Name: user_input_requests user_input_requests_responded_by_channel_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_input_requests
    ADD CONSTRAINT user_input_requests_responded_by_channel_identity_id_fkey FOREIGN KEY (team_id, responded_by_channel_identity_id) REFERENCES public.channel_identities(team_id, id) ON DELETE SET NULL (responded_by_channel_identity_id);


--
-- Name: user_input_requests user_input_requests_route_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_input_requests
    ADD CONSTRAINT user_input_requests_route_id_fkey FOREIGN KEY (team_id, route_id) REFERENCES public.bot_channel_routes(team_id, id) ON DELETE SET NULL (route_id);


--
-- Name: user_input_requests user_input_requests_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_input_requests
    ADD CONSTRAINT user_input_requests_session_id_fkey FOREIGN KEY (team_id, session_id) REFERENCES public.bot_sessions(team_id, id) ON DELETE CASCADE;


--
-- Name: user_input_requests user_input_requests_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_input_requests
    ADD CONSTRAINT user_input_requests_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: user_input_requests user_input_requests_tool_result_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_input_requests
    ADD CONSTRAINT user_input_requests_tool_result_message_id_fkey FOREIGN KEY (team_id, tool_result_message_id) REFERENCES public.bot_history_messages(team_id, id) ON DELETE SET NULL (tool_result_message_id);


--
-- Name: user_provider_oauth_tokens user_provider_oauth_tokens_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_provider_oauth_tokens
    ADD CONSTRAINT user_provider_oauth_tokens_provider_id_fkey FOREIGN KEY (team_id, provider_id) REFERENCES public.providers(team_id, id) ON DELETE CASCADE;


--
-- Name: user_provider_oauth_tokens user_provider_oauth_tokens_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_provider_oauth_tokens
    ADD CONSTRAINT user_provider_oauth_tokens_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: user_provider_oauth_tokens user_provider_oauth_tokens_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_provider_oauth_tokens
    ADD CONSTRAINT user_provider_oauth_tokens_user_id_fkey FOREIGN KEY (team_id, user_id) REFERENCES public.team_members(team_id, user_id) ON DELETE CASCADE;


--
-- Name: user_runtimes user_runtimes_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_runtimes
    ADD CONSTRAINT user_runtimes_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: user_runtimes user_runtimes_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_runtimes
    ADD CONSTRAINT user_runtimes_user_id_fkey FOREIGN KEY (team_id, user_id) REFERENCES public.team_members(team_id, user_id) ON DELETE CASCADE;


--
-- Name: provider_template_models provider_template_models_provider_template_id_fkey; Type: FK CONSTRAINT; Schema: template; Owner: -
--

ALTER TABLE ONLY template.provider_template_models
    ADD CONSTRAINT provider_template_models_provider_template_id_fkey FOREIGN KEY (provider_template_id) REFERENCES template.provider_templates(id) ON DELETE CASCADE;


--
-- Name: bot_acl_rules; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_acl_rules ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_acl_rules bot_acl_rules_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_acl_rules_team_delete ON public.bot_acl_rules FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_acl_rules bot_acl_rules_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_acl_rules_team_insert ON public.bot_acl_rules FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_acl_rules bot_acl_rules_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_acl_rules_team_select ON public.bot_acl_rules FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_acl_rules bot_acl_rules_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_acl_rules_team_update ON public.bot_acl_rules FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_channel_admins; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_channel_admins ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_channel_admins bot_channel_admins_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_channel_admins_team_delete ON public.bot_channel_admins FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_channel_admins bot_channel_admins_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_channel_admins_team_insert ON public.bot_channel_admins FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_channel_admins bot_channel_admins_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_channel_admins_team_select ON public.bot_channel_admins FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_channel_admins bot_channel_admins_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_channel_admins_team_update ON public.bot_channel_admins FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_channel_configs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_channel_configs ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_channel_configs bot_channel_configs_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_channel_configs_team_delete ON public.bot_channel_configs FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_channel_configs bot_channel_configs_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_channel_configs_team_insert ON public.bot_channel_configs FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_channel_configs bot_channel_configs_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_channel_configs_team_select ON public.bot_channel_configs FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_channel_configs bot_channel_configs_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_channel_configs_team_update ON public.bot_channel_configs FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_channel_routes; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_channel_routes ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_channel_routes bot_channel_routes_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_channel_routes_team_delete ON public.bot_channel_routes FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_channel_routes bot_channel_routes_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_channel_routes_team_insert ON public.bot_channel_routes FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_channel_routes bot_channel_routes_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_channel_routes_team_select ON public.bot_channel_routes FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_channel_routes bot_channel_routes_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_channel_routes_team_update ON public.bot_channel_routes FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_email_bindings; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_email_bindings ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_email_bindings bot_email_bindings_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_email_bindings_team_delete ON public.bot_email_bindings FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_email_bindings bot_email_bindings_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_email_bindings_team_insert ON public.bot_email_bindings FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_email_bindings bot_email_bindings_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_email_bindings_team_select ON public.bot_email_bindings FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_email_bindings bot_email_bindings_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_email_bindings_team_update ON public.bot_email_bindings FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_heartbeat_logs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_heartbeat_logs ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_heartbeat_logs bot_heartbeat_logs_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_heartbeat_logs_team_delete ON public.bot_heartbeat_logs FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_heartbeat_logs bot_heartbeat_logs_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_heartbeat_logs_team_insert ON public.bot_heartbeat_logs FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_heartbeat_logs bot_heartbeat_logs_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_heartbeat_logs_team_select ON public.bot_heartbeat_logs FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_heartbeat_logs bot_heartbeat_logs_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_heartbeat_logs_team_update ON public.bot_heartbeat_logs FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_history_message_assets; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_history_message_assets ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_history_message_assets bot_history_message_assets_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_history_message_assets_team_delete ON public.bot_history_message_assets FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_history_message_assets bot_history_message_assets_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_history_message_assets_team_insert ON public.bot_history_message_assets FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_history_message_assets bot_history_message_assets_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_history_message_assets_team_select ON public.bot_history_message_assets FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_history_message_assets bot_history_message_assets_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_history_message_assets_team_update ON public.bot_history_message_assets FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_history_message_compacts; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_history_message_compacts ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_history_message_compacts bot_history_message_compacts_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_history_message_compacts_team_delete ON public.bot_history_message_compacts FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_history_message_compacts bot_history_message_compacts_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_history_message_compacts_team_insert ON public.bot_history_message_compacts FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_history_message_compacts bot_history_message_compacts_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_history_message_compacts_team_select ON public.bot_history_message_compacts FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_history_message_compacts bot_history_message_compacts_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_history_message_compacts_team_update ON public.bot_history_message_compacts FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_history_messages; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_history_messages ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_history_messages bot_history_messages_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_history_messages_team_delete ON public.bot_history_messages FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_history_messages bot_history_messages_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_history_messages_team_insert ON public.bot_history_messages FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_history_messages bot_history_messages_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_history_messages_team_select ON public.bot_history_messages FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_history_messages bot_history_messages_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_history_messages_team_update ON public.bot_history_messages FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_plugin_installations; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_plugin_installations ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_plugin_installations bot_plugin_installations_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_plugin_installations_team_delete ON public.bot_plugin_installations FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_plugin_installations bot_plugin_installations_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_plugin_installations_team_insert ON public.bot_plugin_installations FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_plugin_installations bot_plugin_installations_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_plugin_installations_team_select ON public.bot_plugin_installations FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_plugin_installations bot_plugin_installations_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_plugin_installations_team_update ON public.bot_plugin_installations FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_plugin_resources; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_plugin_resources ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_plugin_resources bot_plugin_resources_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_plugin_resources_team_delete ON public.bot_plugin_resources FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_plugin_resources bot_plugin_resources_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_plugin_resources_team_insert ON public.bot_plugin_resources FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_plugin_resources bot_plugin_resources_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_plugin_resources_team_select ON public.bot_plugin_resources FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_plugin_resources bot_plugin_resources_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_plugin_resources_team_update ON public.bot_plugin_resources FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_remote_runtime_bindings; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_remote_runtime_bindings ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_remote_runtime_bindings bot_remote_runtime_bindings_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_remote_runtime_bindings_team_delete ON public.bot_remote_runtime_bindings FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_remote_runtime_bindings bot_remote_runtime_bindings_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_remote_runtime_bindings_team_insert ON public.bot_remote_runtime_bindings FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_remote_runtime_bindings bot_remote_runtime_bindings_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_remote_runtime_bindings_team_select ON public.bot_remote_runtime_bindings FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_remote_runtime_bindings bot_remote_runtime_bindings_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_remote_runtime_bindings_team_update ON public.bot_remote_runtime_bindings FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_session_discuss_cursors; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_session_discuss_cursors ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_session_discuss_cursors bot_session_discuss_cursors_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_session_discuss_cursors_team_delete ON public.bot_session_discuss_cursors FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_session_discuss_cursors bot_session_discuss_cursors_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_session_discuss_cursors_team_insert ON public.bot_session_discuss_cursors FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_session_discuss_cursors bot_session_discuss_cursors_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_session_discuss_cursors_team_select ON public.bot_session_discuss_cursors FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_session_discuss_cursors bot_session_discuss_cursors_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_session_discuss_cursors_team_update ON public.bot_session_discuss_cursors FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_session_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_session_events ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_session_events bot_session_events_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_session_events_team_delete ON public.bot_session_events FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_session_events bot_session_events_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_session_events_team_insert ON public.bot_session_events FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_session_events bot_session_events_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_session_events_team_select ON public.bot_session_events FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_session_events bot_session_events_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_session_events_team_update ON public.bot_session_events FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_sessions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_sessions ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_sessions bot_sessions_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_sessions_team_delete ON public.bot_sessions FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_sessions bot_sessions_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_sessions_team_insert ON public.bot_sessions FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_sessions bot_sessions_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_sessions_team_select ON public.bot_sessions FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_sessions bot_sessions_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_sessions_team_update ON public.bot_sessions FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_storage_bindings; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_storage_bindings ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_storage_bindings bot_storage_bindings_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_storage_bindings_team_delete ON public.bot_storage_bindings FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_storage_bindings bot_storage_bindings_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_storage_bindings_team_insert ON public.bot_storage_bindings FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_storage_bindings bot_storage_bindings_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_storage_bindings_team_select ON public.bot_storage_bindings FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_storage_bindings bot_storage_bindings_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_storage_bindings_team_update ON public.bot_storage_bindings FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_user_grants; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_user_grants ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_user_grants bot_user_grants_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_user_grants_team_delete ON public.bot_user_grants FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_user_grants bot_user_grants_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_user_grants_team_insert ON public.bot_user_grants FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_user_grants bot_user_grants_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_user_grants_team_select ON public.bot_user_grants FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_user_grants bot_user_grants_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_user_grants_team_update ON public.bot_user_grants FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_workspace_resource_limits; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bot_workspace_resource_limits ENABLE ROW LEVEL SECURITY;

--
-- Name: bot_workspace_resource_limits bot_workspace_resource_limits_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_workspace_resource_limits_team_delete ON public.bot_workspace_resource_limits FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_workspace_resource_limits bot_workspace_resource_limits_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_workspace_resource_limits_team_insert ON public.bot_workspace_resource_limits FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_workspace_resource_limits bot_workspace_resource_limits_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_workspace_resource_limits_team_select ON public.bot_workspace_resource_limits FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bot_workspace_resource_limits bot_workspace_resource_limits_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bot_workspace_resource_limits_team_update ON public.bot_workspace_resource_limits FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bots; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;

--
-- Name: bots bots_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bots_team_delete ON public.bots FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bots bots_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bots_team_insert ON public.bots FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: bots bots_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bots_team_select ON public.bots FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: bots bots_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY bots_team_update ON public.bots FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: channel_identities; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.channel_identities ENABLE ROW LEVEL SECURITY;

--
-- Name: channel_identities channel_identities_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY channel_identities_team_delete ON public.channel_identities FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: channel_identities channel_identities_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY channel_identities_team_insert ON public.channel_identities FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: channel_identities channel_identities_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY channel_identities_team_select ON public.channel_identities FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: channel_identities channel_identities_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY channel_identities_team_update ON public.channel_identities FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: channel_link_codes; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.channel_link_codes ENABLE ROW LEVEL SECURITY;

--
-- Name: channel_link_codes channel_link_codes_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY channel_link_codes_team_delete ON public.channel_link_codes FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: channel_link_codes channel_link_codes_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY channel_link_codes_team_insert ON public.channel_link_codes FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: channel_link_codes channel_link_codes_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY channel_link_codes_team_select ON public.channel_link_codes FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: channel_link_codes channel_link_codes_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY channel_link_codes_team_update ON public.channel_link_codes FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: container_versions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.container_versions ENABLE ROW LEVEL SECURITY;

--
-- Name: container_versions container_versions_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY container_versions_team_delete ON public.container_versions FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: container_versions container_versions_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY container_versions_team_insert ON public.container_versions FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: container_versions container_versions_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY container_versions_team_select ON public.container_versions FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: container_versions container_versions_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY container_versions_team_update ON public.container_versions FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: containers; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.containers ENABLE ROW LEVEL SECURITY;

--
-- Name: containers containers_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY containers_team_delete ON public.containers FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: containers containers_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY containers_team_insert ON public.containers FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: containers containers_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY containers_team_select ON public.containers FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: containers containers_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY containers_team_update ON public.containers FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: email_oauth_tokens; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.email_oauth_tokens ENABLE ROW LEVEL SECURITY;

--
-- Name: email_oauth_tokens email_oauth_tokens_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY email_oauth_tokens_team_delete ON public.email_oauth_tokens FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: email_oauth_tokens email_oauth_tokens_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY email_oauth_tokens_team_insert ON public.email_oauth_tokens FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: email_oauth_tokens email_oauth_tokens_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY email_oauth_tokens_team_select ON public.email_oauth_tokens FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: email_oauth_tokens email_oauth_tokens_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY email_oauth_tokens_team_update ON public.email_oauth_tokens FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: email_outbox; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.email_outbox ENABLE ROW LEVEL SECURITY;

--
-- Name: email_outbox email_outbox_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY email_outbox_team_delete ON public.email_outbox FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: email_outbox email_outbox_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY email_outbox_team_insert ON public.email_outbox FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: email_outbox email_outbox_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY email_outbox_team_select ON public.email_outbox FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: email_outbox email_outbox_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY email_outbox_team_update ON public.email_outbox FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: email_providers; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.email_providers ENABLE ROW LEVEL SECURITY;

--
-- Name: email_providers email_providers_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY email_providers_team_delete ON public.email_providers FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: email_providers email_providers_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY email_providers_team_insert ON public.email_providers FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: email_providers email_providers_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY email_providers_team_select ON public.email_providers FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: email_providers email_providers_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY email_providers_team_update ON public.email_providers FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: fetch_providers; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.fetch_providers ENABLE ROW LEVEL SECURITY;

--
-- Name: fetch_providers fetch_providers_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY fetch_providers_team_delete ON public.fetch_providers FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: fetch_providers fetch_providers_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY fetch_providers_team_insert ON public.fetch_providers FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: fetch_providers fetch_providers_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY fetch_providers_team_select ON public.fetch_providers FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: fetch_providers fetch_providers_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY fetch_providers_team_update ON public.fetch_providers FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: lifecycle_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.lifecycle_events ENABLE ROW LEVEL SECURITY;

--
-- Name: lifecycle_events lifecycle_events_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lifecycle_events_team_delete ON public.lifecycle_events FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: lifecycle_events lifecycle_events_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lifecycle_events_team_insert ON public.lifecycle_events FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: lifecycle_events lifecycle_events_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lifecycle_events_team_select ON public.lifecycle_events FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: lifecycle_events lifecycle_events_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lifecycle_events_team_update ON public.lifecycle_events FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: mcp_connections; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.mcp_connections ENABLE ROW LEVEL SECURITY;

--
-- Name: mcp_connections mcp_connections_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY mcp_connections_team_delete ON public.mcp_connections FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: mcp_connections mcp_connections_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY mcp_connections_team_insert ON public.mcp_connections FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: mcp_connections mcp_connections_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY mcp_connections_team_select ON public.mcp_connections FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: mcp_connections mcp_connections_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY mcp_connections_team_update ON public.mcp_connections FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: mcp_oauth_tokens; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.mcp_oauth_tokens ENABLE ROW LEVEL SECURITY;

--
-- Name: mcp_oauth_tokens mcp_oauth_tokens_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY mcp_oauth_tokens_team_delete ON public.mcp_oauth_tokens FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: mcp_oauth_tokens mcp_oauth_tokens_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY mcp_oauth_tokens_team_insert ON public.mcp_oauth_tokens FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: mcp_oauth_tokens mcp_oauth_tokens_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY mcp_oauth_tokens_team_select ON public.mcp_oauth_tokens FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: mcp_oauth_tokens mcp_oauth_tokens_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY mcp_oauth_tokens_team_update ON public.mcp_oauth_tokens FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: media_assets; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.media_assets ENABLE ROW LEVEL SECURITY;

--
-- Name: media_assets media_assets_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY media_assets_team_delete ON public.media_assets FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: media_assets media_assets_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY media_assets_team_insert ON public.media_assets FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: media_assets media_assets_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY media_assets_team_select ON public.media_assets FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: media_assets media_assets_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY media_assets_team_update ON public.media_assets FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: memory_edges; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.memory_edges ENABLE ROW LEVEL SECURITY;

--
-- Name: memory_edges memory_edges_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY memory_edges_team_delete ON public.memory_edges FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: memory_edges memory_edges_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY memory_edges_team_insert ON public.memory_edges FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: memory_edges memory_edges_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY memory_edges_team_select ON public.memory_edges FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: memory_edges memory_edges_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY memory_edges_team_update ON public.memory_edges FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: memory_nodes; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.memory_nodes ENABLE ROW LEVEL SECURITY;

--
-- Name: memory_nodes memory_nodes_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY memory_nodes_team_delete ON public.memory_nodes FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: memory_nodes memory_nodes_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY memory_nodes_team_insert ON public.memory_nodes FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: memory_nodes memory_nodes_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY memory_nodes_team_select ON public.memory_nodes FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: memory_nodes memory_nodes_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY memory_nodes_team_update ON public.memory_nodes FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: memory_providers; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.memory_providers ENABLE ROW LEVEL SECURITY;

--
-- Name: memory_providers memory_providers_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY memory_providers_team_delete ON public.memory_providers FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: memory_providers memory_providers_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY memory_providers_team_insert ON public.memory_providers FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: memory_providers memory_providers_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY memory_providers_team_select ON public.memory_providers FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: memory_providers memory_providers_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY memory_providers_team_update ON public.memory_providers FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: model_variants; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.model_variants ENABLE ROW LEVEL SECURITY;

--
-- Name: model_variants model_variants_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY model_variants_team_delete ON public.model_variants FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: model_variants model_variants_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY model_variants_team_insert ON public.model_variants FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: model_variants model_variants_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY model_variants_team_select ON public.model_variants FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: model_variants model_variants_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY model_variants_team_update ON public.model_variants FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: models; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.models ENABLE ROW LEVEL SECURITY;

--
-- Name: models models_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY models_team_delete ON public.models FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: models models_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY models_team_insert ON public.models FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: models models_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY models_team_select ON public.models FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: models models_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY models_team_update ON public.models FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: provider_oauth_tokens; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.provider_oauth_tokens ENABLE ROW LEVEL SECURITY;

--
-- Name: provider_oauth_tokens provider_oauth_tokens_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY provider_oauth_tokens_team_delete ON public.provider_oauth_tokens FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: provider_oauth_tokens provider_oauth_tokens_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY provider_oauth_tokens_team_insert ON public.provider_oauth_tokens FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: provider_oauth_tokens provider_oauth_tokens_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY provider_oauth_tokens_team_select ON public.provider_oauth_tokens FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: provider_oauth_tokens provider_oauth_tokens_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY provider_oauth_tokens_team_update ON public.provider_oauth_tokens FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: providers; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.providers ENABLE ROW LEVEL SECURITY;

--
-- Name: providers providers_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY providers_team_delete ON public.providers FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: providers providers_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY providers_team_insert ON public.providers FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: providers providers_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY providers_team_select ON public.providers FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: providers providers_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY providers_team_update ON public.providers FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: schedule; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.schedule ENABLE ROW LEVEL SECURITY;

--
-- Name: schedule_logs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.schedule_logs ENABLE ROW LEVEL SECURITY;

--
-- Name: schedule_logs schedule_logs_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY schedule_logs_team_delete ON public.schedule_logs FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: schedule_logs schedule_logs_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY schedule_logs_team_insert ON public.schedule_logs FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: schedule_logs schedule_logs_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY schedule_logs_team_select ON public.schedule_logs FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: schedule_logs schedule_logs_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY schedule_logs_team_update ON public.schedule_logs FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: schedule schedule_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY schedule_team_delete ON public.schedule FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: schedule schedule_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY schedule_team_insert ON public.schedule FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: schedule schedule_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY schedule_team_select ON public.schedule FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: schedule schedule_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY schedule_team_update ON public.schedule FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: search_providers; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.search_providers ENABLE ROW LEVEL SECURITY;

--
-- Name: search_providers search_providers_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY search_providers_team_delete ON public.search_providers FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: search_providers search_providers_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY search_providers_team_insert ON public.search_providers FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: search_providers search_providers_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY search_providers_team_select ON public.search_providers FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: search_providers search_providers_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY search_providers_team_update ON public.search_providers FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: snapshots; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.snapshots ENABLE ROW LEVEL SECURITY;

--
-- Name: snapshots snapshots_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY snapshots_team_delete ON public.snapshots FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: snapshots snapshots_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY snapshots_team_insert ON public.snapshots FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: snapshots snapshots_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY snapshots_team_select ON public.snapshots FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: snapshots snapshots_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY snapshots_team_update ON public.snapshots FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: storage_providers; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.storage_providers ENABLE ROW LEVEL SECURITY;

--
-- Name: storage_providers storage_providers_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY storage_providers_team_delete ON public.storage_providers FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: storage_providers storage_providers_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY storage_providers_team_insert ON public.storage_providers FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: storage_providers storage_providers_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY storage_providers_team_select ON public.storage_providers FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: storage_providers storage_providers_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY storage_providers_team_update ON public.storage_providers FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: subagent_configs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.subagent_configs ENABLE ROW LEVEL SECURITY;

--
-- Name: subagent_configs subagent_configs_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY subagent_configs_team_delete ON public.subagent_configs FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: subagent_configs subagent_configs_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY subagent_configs_team_insert ON public.subagent_configs FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: subagent_configs subagent_configs_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY subagent_configs_team_select ON public.subagent_configs FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: subagent_configs subagent_configs_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY subagent_configs_team_update ON public.subagent_configs FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: tasks; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tasks ENABLE ROW LEVEL SECURITY;

--
-- Name: tasks tasks_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tasks_team_delete ON public.tasks FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: tasks tasks_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tasks_team_insert ON public.tasks FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: tasks tasks_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tasks_team_select ON public.tasks FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: tasks tasks_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tasks_team_update ON public.tasks FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: team_members; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.team_members ENABLE ROW LEVEL SECURITY;

--
-- Name: team_members team_members_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_members_team_delete ON public.team_members FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: team_members team_members_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_members_team_insert ON public.team_members FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: team_members team_members_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_members_team_select ON public.team_members FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: team_members team_members_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_members_team_update ON public.team_members FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: teams; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.teams ENABLE ROW LEVEL SECURITY;

--
-- Name: teams teams_self_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY teams_self_select ON public.teams FOR SELECT USING ((id = public.memoh_current_team_id()));


--
-- Name: tool_approval_requests; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tool_approval_requests ENABLE ROW LEVEL SECURITY;

--
-- Name: tool_approval_requests tool_approval_requests_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tool_approval_requests_team_delete ON public.tool_approval_requests FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: tool_approval_requests tool_approval_requests_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tool_approval_requests_team_insert ON public.tool_approval_requests FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: tool_approval_requests tool_approval_requests_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tool_approval_requests_team_select ON public.tool_approval_requests FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: tool_approval_requests tool_approval_requests_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tool_approval_requests_team_update ON public.tool_approval_requests FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: tts_models; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tts_models ENABLE ROW LEVEL SECURITY;

--
-- Name: tts_models tts_models_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tts_models_team_delete ON public.tts_models FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: tts_models tts_models_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tts_models_team_insert ON public.tts_models FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: tts_models tts_models_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tts_models_team_select ON public.tts_models FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: tts_models tts_models_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tts_models_team_update ON public.tts_models FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: tts_providers; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tts_providers ENABLE ROW LEVEL SECURITY;

--
-- Name: tts_providers tts_providers_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tts_providers_team_delete ON public.tts_providers FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: tts_providers tts_providers_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tts_providers_team_insert ON public.tts_providers FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: tts_providers tts_providers_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tts_providers_team_select ON public.tts_providers FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: tts_providers tts_providers_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tts_providers_team_update ON public.tts_providers FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: user_channel_bindings; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.user_channel_bindings ENABLE ROW LEVEL SECURITY;

--
-- Name: user_channel_bindings user_channel_bindings_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_channel_bindings_team_delete ON public.user_channel_bindings FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: user_channel_bindings user_channel_bindings_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_channel_bindings_team_insert ON public.user_channel_bindings FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: user_channel_bindings user_channel_bindings_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_channel_bindings_team_select ON public.user_channel_bindings FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: user_channel_bindings user_channel_bindings_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_channel_bindings_team_update ON public.user_channel_bindings FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: user_channel_identity_bindings; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.user_channel_identity_bindings ENABLE ROW LEVEL SECURITY;

--
-- Name: user_channel_identity_bindings user_channel_identity_bindings_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_channel_identity_bindings_team_delete ON public.user_channel_identity_bindings FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: user_channel_identity_bindings user_channel_identity_bindings_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_channel_identity_bindings_team_insert ON public.user_channel_identity_bindings FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: user_channel_identity_bindings user_channel_identity_bindings_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_channel_identity_bindings_team_select ON public.user_channel_identity_bindings FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: user_channel_identity_bindings user_channel_identity_bindings_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_channel_identity_bindings_team_update ON public.user_channel_identity_bindings FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: user_input_requests; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.user_input_requests ENABLE ROW LEVEL SECURITY;

--
-- Name: user_input_requests user_input_requests_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_input_requests_team_delete ON public.user_input_requests FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: user_input_requests user_input_requests_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_input_requests_team_insert ON public.user_input_requests FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: user_input_requests user_input_requests_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_input_requests_team_select ON public.user_input_requests FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: user_input_requests user_input_requests_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_input_requests_team_update ON public.user_input_requests FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: user_provider_oauth_tokens; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.user_provider_oauth_tokens ENABLE ROW LEVEL SECURITY;

--
-- Name: user_provider_oauth_tokens user_provider_oauth_tokens_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_provider_oauth_tokens_team_delete ON public.user_provider_oauth_tokens FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: user_provider_oauth_tokens user_provider_oauth_tokens_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_provider_oauth_tokens_team_insert ON public.user_provider_oauth_tokens FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: user_provider_oauth_tokens user_provider_oauth_tokens_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_provider_oauth_tokens_team_select ON public.user_provider_oauth_tokens FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: user_provider_oauth_tokens user_provider_oauth_tokens_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_provider_oauth_tokens_team_update ON public.user_provider_oauth_tokens FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: user_runtimes; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.user_runtimes ENABLE ROW LEVEL SECURITY;

--
-- Name: user_runtimes user_runtimes_team_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_runtimes_team_delete ON public.user_runtimes FOR DELETE USING ((team_id = public.memoh_current_team_id()));


--
-- Name: user_runtimes user_runtimes_team_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_runtimes_team_insert ON public.user_runtimes FOR INSERT WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- Name: user_runtimes user_runtimes_team_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_runtimes_team_select ON public.user_runtimes FOR SELECT USING ((team_id = public.memoh_current_team_id()));


--
-- Name: user_runtimes user_runtimes_team_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_runtimes_team_update ON public.user_runtimes FOR UPDATE USING ((team_id = public.memoh_current_team_id())) WITH CHECK ((team_id = public.memoh_current_team_id()));


--
-- PostgreSQL database dump complete
--


