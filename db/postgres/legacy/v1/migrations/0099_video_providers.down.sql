-- 0099_video_providers
-- Remove video provider/client types and per-bot video model selection.

UPDATE bots SET video_model_id = NULL;
ALTER TABLE bots DROP COLUMN IF EXISTS video_model_id;

DELETE FROM models WHERE type = 'video';
DELETE FROM providers WHERE client_type IN ('openrouter-video', 'modelark-video', 'volcengine-video');

ALTER TABLE models DROP CONSTRAINT IF EXISTS models_type_check;
ALTER TABLE models ADD CONSTRAINT models_type_check CHECK (type IN ('chat', 'embedding', 'speech', 'transcription'));

ALTER TABLE providers DROP CONSTRAINT IF EXISTS providers_client_type_check;
ALTER TABLE providers ADD CONSTRAINT providers_client_type_check CHECK (client_type IN (
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
