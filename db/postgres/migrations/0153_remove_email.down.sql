-- 0153_remove_email
-- Restore the previous email schema. Deleted data cannot be recovered.

CREATE TABLE IF NOT EXISTS public.email_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT email_providers_pkey PRIMARY KEY (id),
    CONSTRAINT email_providers_user_name_unique UNIQUE (team_id, user_id, name),
    CONSTRAINT memoh_team_key_41adeda78ae7 UNIQUE (team_id, id),
    CONSTRAINT email_providers_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT,
    CONSTRAINT email_providers_user_id_fkey FOREIGN KEY (team_id, user_id) REFERENCES public.team_members(team_id, user_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS public.email_oauth_tokens (
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
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT email_oauth_tokens_email_provider_id_key UNIQUE (team_id, email_provider_id),
    CONSTRAINT email_oauth_tokens_pkey PRIMARY KEY (id),
    CONSTRAINT memoh_team_key_0b525bc25d91 UNIQUE (team_id, id),
    CONSTRAINT email_oauth_tokens_email_provider_id_fkey FOREIGN KEY (team_id, email_provider_id) REFERENCES public.email_providers(team_id, id) ON DELETE CASCADE,
    CONSTRAINT email_oauth_tokens_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS public.bot_email_bindings (
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
    team_id uuid DEFAULT public.memoh_current_team_id() NOT NULL,
    CONSTRAINT bot_email_bindings_pkey PRIMARY KEY (id),
    CONSTRAINT bot_email_bindings_unique UNIQUE (team_id, bot_id, email_provider_id),
    CONSTRAINT memoh_team_key_812bc98005b4 UNIQUE (team_id, id),
    CONSTRAINT bot_email_bindings_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE,
    CONSTRAINT bot_email_bindings_email_provider_id_fkey FOREIGN KEY (team_id, email_provider_id) REFERENCES public.email_providers(team_id, id) ON DELETE CASCADE,
    CONSTRAINT bot_email_bindings_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS public.email_outbox (
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
    CONSTRAINT email_outbox_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'sent'::text, 'failed'::text]))),
    CONSTRAINT email_outbox_pkey PRIMARY KEY (id),
    CONSTRAINT memoh_team_key_b6fd1bd87341 UNIQUE (team_id, id),
    CONSTRAINT email_outbox_bot_id_fkey FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE,
    CONSTRAINT email_outbox_provider_id_fkey FOREIGN KEY (team_id, provider_id) REFERENCES public.email_providers(team_id, id) ON DELETE CASCADE,
    CONSTRAINT email_outbox_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_bot_email_bindings_bot_id ON public.bot_email_bindings USING btree (team_id, bot_id);

CREATE INDEX IF NOT EXISTS idx_bot_email_bindings_provider_id ON public.bot_email_bindings USING btree (team_id, email_provider_id);

CREATE INDEX IF NOT EXISTS idx_email_oauth_tokens_state ON public.email_oauth_tokens USING btree (team_id, state) WHERE (state <> ''::text);

CREATE INDEX IF NOT EXISTS idx_email_outbox_bot_id ON public.email_outbox USING btree (team_id, bot_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_email_outbox_provider_id ON public.email_outbox USING btree (team_id, provider_id);

CREATE INDEX IF NOT EXISTS idx_email_providers_user_id ON public.email_providers USING btree (team_id, user_id);

DO $$
DECLARE
    tbl text;
BEGIN
    FOREACH tbl IN ARRAY ARRAY['email_providers', 'email_oauth_tokens', 'bot_email_bindings', 'email_outbox']
    LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tbl);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tbl);
        EXECUTE format('DROP POLICY IF EXISTS %I ON public.%I', tbl || '_team_select', tbl);
        EXECUTE format('DROP POLICY IF EXISTS %I ON public.%I', tbl || '_team_insert', tbl);
        EXECUTE format('DROP POLICY IF EXISTS %I ON public.%I', tbl || '_team_update', tbl);
        EXECUTE format('DROP POLICY IF EXISTS %I ON public.%I', tbl || '_team_delete', tbl);
        EXECUTE format('CREATE POLICY %I ON public.%I FOR SELECT USING (team_id = public.memoh_current_team_id())', tbl || '_team_select', tbl);
        EXECUTE format('CREATE POLICY %I ON public.%I FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id())', tbl || '_team_insert', tbl);
        EXECUTE format('CREATE POLICY %I ON public.%I FOR UPDATE USING (team_id = public.memoh_current_team_id()) WITH CHECK (team_id = public.memoh_current_team_id())', tbl || '_team_update', tbl);
        EXECUTE format('CREATE POLICY %I ON public.%I FOR DELETE USING (team_id = public.memoh_current_team_id())', tbl || '_team_delete', tbl);
    END LOOP;
END
$$;
