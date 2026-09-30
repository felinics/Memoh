-- 0160_chatgpt_provider_sessions
-- Allow ChatGPT providers and store their encrypted authorization state.

ALTER TABLE public.providers DROP CONSTRAINT IF EXISTS providers_client_type_check;
ALTER TABLE public.providers ADD CONSTRAINT providers_client_type_check CHECK (client_type IN (
    'openai-responses',
    'openai-completions',
    'anthropic-messages',
    'google-generative-ai',
    'openai-codex',
    'openai-chatgpt',
    'github-copilot',
    'edge-speech',
    'openai-speech',
    'openai-transcription',
    'openrouter-speech',
    'openrouter-transcription',
    'elevenlabs-speech',
    'elevenlabs-transcription',
    'deepgram-speech',
    'deepgram-transcription',
    'minimax-speech',
    'volcengine-speech',
    'alibabacloud-speech',
    'alibabacloud-transcription',
    'microsoft-speech',
    'google-speech',
    'google-transcription',
    'openrouter-video',
    'modelark-video',
    'volcengine-video'
  ));

-- ChatGPT subscription registration and encrypted session state.
CREATE TABLE IF NOT EXISTS public.chatgpt_runtime_hosts (
    team_id UUID PRIMARY KEY DEFAULT public.memoh_current_team_id() REFERENCES public.teams(id) ON DELETE RESTRICT,
    host_id UUID NOT NULL DEFAULT gen_random_uuid()
);
CREATE TABLE IF NOT EXISTS public.chatgpt_provider_sessions (
    team_id UUID NOT NULL DEFAULT public.memoh_current_team_id() REFERENCES public.teams(id) ON DELETE RESTRICT,
    provider_id UUID NOT NULL,
    owner_user_id UUID NOT NULL,
    encrypted_payload BYTEA NOT NULL DEFAULT ''::bytea,
    encryption_nonce BYTEA NOT NULL DEFAULT ''::bytea,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, provider_id),
    FOREIGN KEY (team_id, provider_id) REFERENCES public.providers(team_id, id) ON DELETE CASCADE,
    FOREIGN KEY (team_id, owner_user_id) REFERENCES public.team_members(team_id, user_id) ON DELETE RESTRICT,
    CHECK ((octet_length(encrypted_payload) = 0 AND octet_length(encryption_nonce) = 0) OR
           (octet_length(encrypted_payload) > 0 AND octet_length(encryption_nonce) = 12))
);
ALTER TABLE public.chatgpt_runtime_hosts ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.chatgpt_runtime_hosts FORCE ROW LEVEL SECURITY;
ALTER TABLE public.chatgpt_provider_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.chatgpt_provider_sessions FORCE ROW LEVEL SECURITY;
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = 'public' AND tablename = 'chatgpt_runtime_hosts' AND policyname = 'chatgpt_runtime_hosts_team') THEN
        CREATE POLICY chatgpt_runtime_hosts_team ON public.chatgpt_runtime_hosts
            USING (team_id = public.memoh_current_team_id()) WITH CHECK (team_id = public.memoh_current_team_id());
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = 'public' AND tablename = 'chatgpt_provider_sessions' AND policyname = 'chatgpt_provider_sessions_team') THEN
        CREATE POLICY chatgpt_provider_sessions_team ON public.chatgpt_provider_sessions
            USING (team_id = public.memoh_current_team_id()) WITH CHECK (team_id = public.memoh_current_team_id());
    END IF;
END $$;
