-- 0153_opencode_go
-- Restore the previous provider types without deleting configured providers.
-- OpenCode Go cannot be converted to a single legacy protocol safely.

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM public.providers WHERE client_type = 'opencode-go') THEN
    RAISE EXCEPTION 'Remove OpenCode Go providers before rolling back migration 0153';
  END IF;
END $$;

ALTER TABLE public.providers DROP CONSTRAINT IF EXISTS providers_client_type_check;
ALTER TABLE public.providers ADD CONSTRAINT providers_client_type_check CHECK (client_type IN (
    'openai-responses',
    'openai-completions',
    'anthropic-messages',
    'google-generative-ai',
    'openai-codex',
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
