-- 0071_split_transcription_providers
-- Remove dedicated transcription provider client types.

DELETE FROM providers
WHERE client_type IN (
  'openai-transcription',
  'openrouter-transcription',
  'elevenlabs-transcription',
  'deepgram-transcription',
  'google-transcription'
);

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
  'openrouter-speech',
  'elevenlabs-speech',
  'deepgram-speech',
  'minimax-speech',
  'volcengine-speech',
  'alibabacloud-speech',
  'microsoft-speech',
  'google-speech'
));
