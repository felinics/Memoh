-- +goose Up
-- +goose StatementBegin
-- Goose SessionLock may SET ROLE memoh_migrate before this script runs.
-- Reset to the deploying login so this owner can normalize its schema and ledger.
RESET ROLE;

CREATE SCHEMA IF NOT EXISTS model;
ALTER SCHEMA model OWNER TO memoh_migrate;

DO $defaults_login$
DECLARE
  login_role text := session_user;
BEGIN
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA model GRANT ALL ON TABLES TO memoh_migrate', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA model GRANT ALL ON SEQUENCES TO memoh_migrate', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA model GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO memoh_model', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA model GRANT USAGE, SELECT ON SEQUENCES TO memoh_model', login_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA model GRANT EXECUTE ON FUNCTIONS TO memoh_model', login_role);
END
$defaults_login$;

DO $version_owner$
BEGIN
  IF to_regclass('model.goose_db_version') IS NOT NULL THEN
    ALTER TABLE model.goose_db_version OWNER TO memoh_migrate;
  END IF;
END
$version_owner$;

SET LOCAL ROLE memoh_migrate;

REVOKE ALL ON SCHEMA model FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA model TO memoh_migrate;
GRANT USAGE ON SCHEMA model TO memoh_model;

ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA model GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO memoh_model;
ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA model GRANT USAGE, SELECT ON SEQUENCES TO memoh_model;
ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA model GRANT EXECUTE ON FUNCTIONS TO memoh_model;

CREATE TABLE model.fetch_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    enable boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY model.fetch_providers FORCE ROW LEVEL SECURITY;

CREATE TABLE model.model_variants (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    model_uuid uuid NOT NULL,
    variant_id text NOT NULL,
    weight integer NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY model.model_variants FORCE ROW LEVEL SECURITY;

CREATE TABLE model.models (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    model_id text NOT NULL,
    name text,
    provider_id uuid NOT NULL,
    type text DEFAULT 'chat'::text NOT NULL,
    enable boolean DEFAULT true NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT models_type_check CHECK ((type = ANY (ARRAY['chat'::text, 'embedding'::text, 'speech'::text, 'transcription'::text, 'video'::text])))
);

ALTER TABLE ONLY model.models FORCE ROW LEVEL SECURITY;

CREATE TABLE model.provider_oauth_tokens (
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
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY model.provider_oauth_tokens FORCE ROW LEVEL SECURITY;

CREATE TABLE model.provider_template_models (
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

CREATE TABLE model.provider_templates (
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

CREATE TABLE model.providers (
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
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL,
    CONSTRAINT providers_client_type_check CHECK ((client_type = ANY (ARRAY['openai-responses'::text, 'openai-completions'::text, 'anthropic-messages'::text, 'google-generative-ai'::text, 'openai-codex'::text, 'github-copilot'::text, 'edge-speech'::text, 'openai-speech'::text, 'openai-transcription'::text, 'openrouter-speech'::text, 'openrouter-transcription'::text, 'elevenlabs-speech'::text, 'elevenlabs-transcription'::text, 'deepgram-speech'::text, 'deepgram-transcription'::text, 'minimax-speech'::text, 'volcengine-speech'::text, 'alibabacloud-speech'::text, 'microsoft-speech'::text, 'google-speech'::text, 'google-transcription'::text, 'openrouter-video'::text, 'modelark-video'::text, 'volcengine-video'::text])))
);

ALTER TABLE ONLY model.providers FORCE ROW LEVEL SECURITY;

CREATE TABLE model.search_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    enable boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY model.search_providers FORCE ROW LEVEL SECURITY;

CREATE TABLE model.tts_models (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    model_id text NOT NULL,
    name text,
    tts_provider_id uuid NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY model.tts_models FORCE ROW LEVEL SECURITY;

CREATE TABLE model.tts_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    enable boolean DEFAULT false NOT NULL,
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY model.tts_providers FORCE ROW LEVEL SECURITY;

CREATE TABLE model.user_provider_oauth_tokens (
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
    team_id uuid DEFAULT iam.memoh_current_team_id() NOT NULL
);

ALTER TABLE ONLY model.user_provider_oauth_tokens FORCE ROW LEVEL SECURITY;

ALTER TABLE ONLY model.fetch_providers
    ADD CONSTRAINT fetch_providers_name_unique UNIQUE (team_id, name);

ALTER TABLE ONLY model.fetch_providers
    ADD CONSTRAINT fetch_providers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY model.models
    ADD CONSTRAINT memoh_team_key_3b9411fe39b8 UNIQUE (team_id, id);

ALTER TABLE ONLY model.providers
    ADD CONSTRAINT memoh_team_key_3ff2063985e5 UNIQUE (team_id, id);

ALTER TABLE ONLY model.search_providers
    ADD CONSTRAINT memoh_team_key_512d51a6668f UNIQUE (team_id, id);

ALTER TABLE ONLY model.tts_models
    ADD CONSTRAINT memoh_team_key_5a0d5daa7bc4 UNIQUE (team_id, id);

ALTER TABLE ONLY model.fetch_providers
    ADD CONSTRAINT memoh_team_key_60114d54331f UNIQUE (team_id, id);

ALTER TABLE ONLY model.user_provider_oauth_tokens
    ADD CONSTRAINT memoh_team_key_65ba82d99f4b UNIQUE (team_id, id);

ALTER TABLE ONLY model.model_variants
    ADD CONSTRAINT memoh_team_key_8351313ed607 UNIQUE (team_id, id);

ALTER TABLE ONLY model.provider_oauth_tokens
    ADD CONSTRAINT memoh_team_key_b1d8ebe8cf22 UNIQUE (team_id, id);

ALTER TABLE ONLY model.tts_providers
    ADD CONSTRAINT memoh_team_key_dd046f675949 UNIQUE (team_id, id);

ALTER TABLE ONLY model.model_variants
    ADD CONSTRAINT model_variants_pkey PRIMARY KEY (id);

ALTER TABLE ONLY model.models
    ADD CONSTRAINT models_pkey PRIMARY KEY (id);

ALTER TABLE ONLY model.models
    ADD CONSTRAINT models_provider_id_model_id_unique UNIQUE (team_id, provider_id, model_id);

ALTER TABLE ONLY model.provider_oauth_tokens
    ADD CONSTRAINT provider_oauth_tokens_pkey PRIMARY KEY (id);

ALTER TABLE ONLY model.provider_oauth_tokens
    ADD CONSTRAINT provider_oauth_tokens_provider_id_key UNIQUE (team_id, provider_id);

ALTER TABLE ONLY model.provider_template_models
    ADD CONSTRAINT provider_template_models_identity_unique UNIQUE (provider_template_id, type, model_id);

ALTER TABLE ONLY model.provider_template_models
    ADD CONSTRAINT provider_template_models_pkey PRIMARY KEY (id);

ALTER TABLE ONLY model.provider_templates
    ADD CONSTRAINT provider_templates_domain_key_unique UNIQUE (domain, key);

ALTER TABLE ONLY model.provider_templates
    ADD CONSTRAINT provider_templates_pkey PRIMARY KEY (id);

ALTER TABLE ONLY model.providers
    ADD CONSTRAINT providers_name_unique UNIQUE (team_id, name);

ALTER TABLE ONLY model.providers
    ADD CONSTRAINT providers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY model.search_providers
    ADD CONSTRAINT search_providers_name_unique UNIQUE (team_id, name);

ALTER TABLE ONLY model.search_providers
    ADD CONSTRAINT search_providers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY model.search_providers
    ADD CONSTRAINT search_providers_provider_unique UNIQUE (team_id, provider);

ALTER TABLE ONLY model.tts_models
    ADD CONSTRAINT tts_models_pkey PRIMARY KEY (id);

ALTER TABLE ONLY model.tts_providers
    ADD CONSTRAINT tts_providers_name_unique UNIQUE (team_id, name);

ALTER TABLE ONLY model.tts_providers
    ADD CONSTRAINT tts_providers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY model.user_provider_oauth_tokens
    ADD CONSTRAINT user_provider_oauth_tokens_pkey PRIMARY KEY (id);

ALTER TABLE ONLY model.user_provider_oauth_tokens
    ADD CONSTRAINT user_provider_oauth_tokens_provider_user_unique UNIQUE (team_id, provider_id, user_id);

CREATE INDEX idx_model_variants_model_uuid ON model.model_variants USING btree (team_id, model_uuid);

CREATE INDEX idx_model_variants_variant_id ON model.model_variants USING btree (team_id, variant_id);

CREATE INDEX idx_provider_oauth_tokens_state ON model.provider_oauth_tokens USING btree (team_id, state) WHERE (state <> ''::text);

CREATE INDEX idx_provider_template_models_template_active_order ON model.provider_template_models USING btree (provider_template_id, active, sort_order, model_id);

CREATE INDEX idx_provider_templates_domain_active_order ON model.provider_templates USING btree (domain, active, sort_order, name);

CREATE INDEX idx_providers_provider_template_id ON model.providers USING btree (team_id, provider_template_id);

CREATE INDEX idx_tts_models_provider_id ON model.tts_models USING btree (team_id, tts_provider_id);

CREATE INDEX idx_user_provider_oauth_tokens_state ON model.user_provider_oauth_tokens USING btree (team_id, state) WHERE (state <> ''::text);

ALTER TABLE ONLY model.model_variants
    ADD CONSTRAINT model_variants_model_uuid_fkey FOREIGN KEY (team_id, model_uuid) REFERENCES model.models(team_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY model.models
    ADD CONSTRAINT models_provider_id_fkey FOREIGN KEY (team_id, provider_id) REFERENCES model.providers(team_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY model.provider_oauth_tokens
    ADD CONSTRAINT provider_oauth_tokens_provider_id_fkey FOREIGN KEY (team_id, provider_id) REFERENCES model.providers(team_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY model.provider_template_models
    ADD CONSTRAINT provider_template_models_provider_template_id_fkey FOREIGN KEY (provider_template_id) REFERENCES model.provider_templates(id) ON DELETE CASCADE;

ALTER TABLE ONLY model.providers
    ADD CONSTRAINT providers_provider_template_id_fkey FOREIGN KEY (provider_template_id) REFERENCES model.provider_templates(id) ON DELETE SET NULL (provider_template_id);

ALTER TABLE ONLY model.tts_models
    ADD CONSTRAINT tts_models_tts_provider_id_fkey FOREIGN KEY (team_id, tts_provider_id) REFERENCES model.tts_providers(team_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY model.user_provider_oauth_tokens
    ADD CONSTRAINT user_provider_oauth_tokens_provider_id_fkey FOREIGN KEY (team_id, provider_id) REFERENCES model.providers(team_id, id) ON DELETE CASCADE;

ALTER TABLE model.fetch_providers ENABLE ROW LEVEL SECURITY;

CREATE POLICY fetch_providers_team_delete ON model.fetch_providers FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY fetch_providers_team_insert ON model.fetch_providers FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY fetch_providers_team_select ON model.fetch_providers FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY fetch_providers_team_update ON model.fetch_providers FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE model.model_variants ENABLE ROW LEVEL SECURITY;

CREATE POLICY model_variants_team_delete ON model.model_variants FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY model_variants_team_insert ON model.model_variants FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY model_variants_team_select ON model.model_variants FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY model_variants_team_update ON model.model_variants FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE model.models ENABLE ROW LEVEL SECURITY;

CREATE POLICY models_team_delete ON model.models FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY models_team_insert ON model.models FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY models_team_select ON model.models FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY models_team_update ON model.models FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE model.provider_oauth_tokens ENABLE ROW LEVEL SECURITY;

CREATE POLICY provider_oauth_tokens_team_delete ON model.provider_oauth_tokens FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY provider_oauth_tokens_team_insert ON model.provider_oauth_tokens FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY provider_oauth_tokens_team_select ON model.provider_oauth_tokens FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY provider_oauth_tokens_team_update ON model.provider_oauth_tokens FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE model.providers ENABLE ROW LEVEL SECURITY;

CREATE POLICY providers_team_delete ON model.providers FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY providers_team_insert ON model.providers FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY providers_team_select ON model.providers FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY providers_team_update ON model.providers FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE model.search_providers ENABLE ROW LEVEL SECURITY;

CREATE POLICY search_providers_team_delete ON model.search_providers FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY search_providers_team_insert ON model.search_providers FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY search_providers_team_select ON model.search_providers FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY search_providers_team_update ON model.search_providers FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE model.tts_models ENABLE ROW LEVEL SECURITY;

CREATE POLICY tts_models_team_delete ON model.tts_models FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY tts_models_team_insert ON model.tts_models FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY tts_models_team_select ON model.tts_models FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY tts_models_team_update ON model.tts_models FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE model.tts_providers ENABLE ROW LEVEL SECURITY;

CREATE POLICY tts_providers_team_delete ON model.tts_providers FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY tts_providers_team_insert ON model.tts_providers FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY tts_providers_team_select ON model.tts_providers FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY tts_providers_team_update ON model.tts_providers FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

ALTER TABLE model.user_provider_oauth_tokens ENABLE ROW LEVEL SECURITY;

CREATE POLICY user_provider_oauth_tokens_team_delete ON model.user_provider_oauth_tokens FOR DELETE USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY user_provider_oauth_tokens_team_insert ON model.user_provider_oauth_tokens FOR INSERT WITH CHECK ((team_id = iam.memoh_current_team_id()));

CREATE POLICY user_provider_oauth_tokens_team_select ON model.user_provider_oauth_tokens FOR SELECT USING ((team_id = iam.memoh_current_team_id()));

CREATE POLICY user_provider_oauth_tokens_team_update ON model.user_provider_oauth_tokens FOR UPDATE USING ((team_id = iam.memoh_current_team_id())) WITH CHECK ((team_id = iam.memoh_current_team_id()));

-- Explicit privilege contract for model (business role has DML only; no DDL/BYPASSRLS).
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA model TO memoh_model;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA model TO memoh_model;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA model TO memoh_model;
DO $version_priv$
BEGIN
  IF to_regclass('model.goose_db_version') IS NOT NULL THEN
    ALTER TABLE model.goose_db_version OWNER TO memoh_migrate;
    REVOKE ALL ON TABLE model.goose_db_version FROM PUBLIC;
    GRANT SELECT ON TABLE model.goose_db_version TO memoh_model;
    REVOKE INSERT, UPDATE, DELETE ON TABLE model.goose_db_version FROM memoh_model;
  END IF;
END
$version_priv$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Destructive epoch v2 baseline rollback is unsupported.
SELECT 'epoch_v2_baseline_down_noop'::text;
-- +goose StatementEnd
