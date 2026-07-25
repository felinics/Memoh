-- 0068_expand_speech_provider_client_types (rollback)
-- Remove newly added Twilight speech provider client_type values from unified providers table.

DELETE FROM providers
WHERE client_type IN (
  'openai-speech',
  'openrouter-speech',
  'elevenlabs-speech',
  'deepgram-speech',
  'minimax-speech',
  'volcengine-speech',
  'alibabacloud-speech',
  'microsoft-speech'
);

ALTER TABLE IF EXISTS providers DROP CONSTRAINT IF EXISTS providers_client_type_check;

ALTER TABLE IF EXISTS providers
  ADD CONSTRAINT providers_client_type_check CHECK (
    client_type IN (
      'openai-responses',
      'openai-completions',
      'anthropic-messages',
      'google-generative-ai',
      'openai-codex',
      'github-copilot',
      'edge-speech'
    )
  );
