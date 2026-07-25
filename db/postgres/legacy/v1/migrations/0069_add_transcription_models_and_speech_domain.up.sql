-- 0069_add_transcription_models_and_speech_domain
-- Expand the speech domain to support transcription models and shared speech providers.

ALTER TABLE providers
  DROP CONSTRAINT IF EXISTS providers_client_type_check;

ALTER TABLE providers
  ADD CONSTRAINT providers_client_type_check CHECK (client_type IN (
    'openai-responses',
    'openai-completions',
    'anthropic-messages',
    'google-generative-ai',
    'openai-codex',
    'github-copilot',
    'edge-speech',
    'openai-speech',
    'openrouter-speech',
    'elevenlabs-speech',
    'deepgram-speech',
    'minimax-speech',
    'volcengine-speech',
    'alibabacloud-speech',
    'microsoft-speech',
    'google-speech'
  ));

ALTER TABLE models
  DROP CONSTRAINT IF EXISTS models_type_check;

ALTER TABLE models
  ADD CONSTRAINT models_type_check CHECK (type IN ('chat', 'embedding', 'speech', 'transcription'));
