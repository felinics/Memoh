export interface ClientTypeMeta {
  value: string
  label: string
  hint: string
}

const MANAGED_OAUTH_CLIENT_TYPES = new Set(['openai-codex', 'github-copilot'])

// These API protocols commonly carry tool calls and reasoning metadata for
// agent models. Custom-provider auto import submits these as an explicit
// fallback when discovery cannot report a model's capabilities. Vision stays
// opt-in because protocol compatibility does not imply image input support.
const AGENT_PROTOCOL_CLIENT_TYPES = new Set([
  'openai-responses',
  'openai-completions',
  'anthropic-messages',
  'google-generative-ai',
])

export function suggestedModelCompatibilities(clientType: unknown): string[] {
  if (typeof clientType === 'string' && AGENT_PROTOCOL_CLIENT_TYPES.has(clientType)) {
    return ['tool-call', 'reasoning']
  }
  return []
}

export function isManagedOAuthClientType(clientType: unknown): boolean {
  return typeof clientType === 'string' && MANAGED_OAUTH_CLIENT_TYPES.has(clientType)
}

export function isManagedModelCatalogClientType(clientType: unknown): boolean {
  return isManagedOAuthClientType(clientType)
}

export const CLIENT_TYPE_META: Record<string, ClientTypeMeta> = {
  'openai-responses': {
    value: 'openai-responses',
    label: 'OpenAI Responses',
    hint: 'Responses API (streaming, built-in tools)',
  },
  'openai-completions': {
    value: 'openai-completions',
    label: 'OpenAI Completions',
    hint: 'Chat Completions API (widely compatible)',
  },
  'openai-codex': {
    value: 'openai-codex',
    label: 'OpenAI Codex',
    hint: 'Codex API (OAuth, coding-optimized)',
  },
  'github-copilot': {
    value: 'github-copilot',
    label: 'GitHub Copilot',
    hint: 'Device OAuth with GitHub account',
  },
  'anthropic-messages': {
    value: 'anthropic-messages',
    label: 'Anthropic Messages',
    hint: 'Messages API (Claude models)',
  },
  'google-generative-ai': {
    value: 'google-generative-ai',
    label: 'Google Generative AI',
    hint: 'Gemini API',
  },
  'edge-speech': {
    value: 'edge-speech',
    label: 'Edge Speech',
    hint: 'Microsoft Edge Read Aloud TTS',
  },
  'openai-speech': {
    value: 'openai-speech',
    label: 'OpenAI Speech',
    hint: 'OpenAI /audio/speech compatible TTS',
  },
  'openai-transcription': {
    value: 'openai-transcription',
    label: 'OpenAI Transcription',
    hint: 'OpenAI audio transcription',
  },
  'openrouter-speech': {
    value: 'openrouter-speech',
    label: 'OpenRouter Speech',
    hint: 'OpenRouter audio modality TTS',
  },
  'openrouter-transcription': {
    value: 'openrouter-transcription',
    label: 'OpenRouter Transcription',
    hint: 'OpenRouter transcription models',
  },
  'elevenlabs-speech': {
    value: 'elevenlabs-speech',
    label: 'ElevenLabs Speech',
    hint: 'ElevenLabs text-to-speech',
  },
  'elevenlabs-transcription': {
    value: 'elevenlabs-transcription',
    label: 'ElevenLabs Transcription',
    hint: 'ElevenLabs speech-to-text',
  },
  'deepgram-speech': {
    value: 'deepgram-speech',
    label: 'Deepgram Speech',
    hint: 'Deepgram TTS',
  },
  'deepgram-transcription': {
    value: 'deepgram-transcription',
    label: 'Deepgram Transcription',
    hint: 'Deepgram speech-to-text',
  },
  'minimax-speech': {
    value: 'minimax-speech',
    label: 'MiniMax Speech',
    hint: 'MiniMax TTS',
  },
  'volcengine-speech': {
    value: 'volcengine-speech',
    label: 'Volcengine Speech',
    hint: 'Volcengine SAMI TTS',
  },
  'alibabacloud-speech': {
    value: 'alibabacloud-speech',
    label: 'Alibaba Cloud Speech',
    hint: 'DashScope CosyVoice TTS',
  },
  'microsoft-speech': {
    value: 'microsoft-speech',
    label: 'Microsoft Speech',
    hint: 'Azure Cognitive Services TTS',
  },
  'google-speech': {
    value: 'google-speech',
    label: 'Google Speech',
    hint: 'Gemini speech transcription',
  },
  'google-transcription': {
    value: 'google-transcription',
    label: 'Google Transcription',
    hint: 'Gemini speech transcription',
  },
  'openrouter-video': {
    value: 'openrouter-video',
    label: 'OpenRouter Video',
    hint: 'OpenRouter video generation',
  },
  'modelark-video': {
    value: 'modelark-video',
    label: 'ModelArk Video',
    hint: 'BytePlus ModelArk video generation',
  },
  'volcengine-video': {
    value: 'volcengine-video',
    label: 'Volcengine Video',
    hint: 'Volcengine Ark video generation',
  },
}

export const CLIENT_TYPE_LIST: ClientTypeMeta[] = Object.values(CLIENT_TYPE_META)

export const LLM_CLIENT_TYPE_LIST: ClientTypeMeta[] = CLIENT_TYPE_LIST
  .filter(ct => !ct.value.endsWith('-speech') && !ct.value.endsWith('-transcription') && !ct.value.endsWith('-video'))

// Managed OAuth providers are created from their dedicated presets. Keeping
// them out of generic selectors prevents partially configured instances.
export const MANUAL_LLM_CLIENT_TYPE_LIST: ClientTypeMeta[] = LLM_CLIENT_TYPE_LIST
  .filter(ct => !isManagedOAuthClientType(ct.value))
