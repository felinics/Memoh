-- 0071_split_transcription_providers
-- Add dedicated transcription provider client types.

ALTER TABLE providers DROP CONSTRAINT IF EXISTS providers_client_type_check;

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
  'microsoft-speech',
  'google-speech',
  'google-transcription'
));
