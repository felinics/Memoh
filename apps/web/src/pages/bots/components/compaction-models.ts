interface CompactionModelLike {
  model_id?: string
  provider_id?: string | null
  enable?: boolean
  config?: {
    context_window?: number | null
    compatibilities?: string[] | null
  } | null
}

interface CompactionProviderLike {
  id?: string | null
  client_type?: string
  enable?: boolean
}

// Mirrors the server gate (models.IsLLMClientType && models.EnforcesMaxOutputTokens):
// text LLM clients that honor an output cap. openai-codex ignores output caps
// and unknown client types fail closed — the resolver would reject both.
const SUMMARIZER_CLIENT_TYPES = new Set([
  'openai-completions',
  'openai-responses',
  'anthropic-messages',
  'google-generative-ai',
  'github-copilot',
])

// Mirrors models.isKnownStandaloneImageModelID: dedicated text-to-image
// families that carry the chat type but cannot summarize. Tool calling is the
// server's escape hatch for name collisions, honored here too. The server
// resolver stays authoritative for provider-URL-based image heuristics.
const IMAGE_MODEL_PREFIXES = [
  'qwen-image',
  'wan2',
  'wanx',
  'z-image',
  'flux-',
  'flux.',
  'flux1',
  'stable-diffusion',
  'gpt-image',
  'dall-e',
]

function isImageOnlyModel(model: CompactionModelLike): boolean {
  if ((model.config?.compatibilities ?? []).includes('tool-call')) {
    return false
  }
  // The family name lives in the last path segment for namespaced IDs like
  // accounts/fireworks/models/flux-1-dev.
  const modelID = (model.model_id ?? '').trim().toLowerCase()
  const base = modelID.slice(modelID.lastIndexOf('/') + 1)
  return IMAGE_MODEL_PREFIXES.some(prefix => base.startsWith(prefix)) || base.includes('seedream')
}

function providerCanSummarize(provider: CompactionProviderLike): boolean {
  return provider.enable !== false && SUMMARIZER_CLIENT_TYPES.has(provider.client_type ?? '')
}

export function filterCompactionModels<T extends CompactionModelLike>(
  models: readonly T[],
  providers: readonly CompactionProviderLike[],
): T[] {
  const eligibleProviderIds = new Set(
    providers
      .filter(providerCanSummarize)
      .map(provider => provider.id)
      .filter((id): id is string => Boolean(id)),
  )

  return models.filter((model) => {
    if (model.enable === false) {
      return false
    }
    if (!model.provider_id || !eligibleProviderIds.has(model.provider_id)) {
      return false
    }
    if (isImageOnlyModel(model)) {
      return false
    }
    // The resolver fails closed on models without a declared context window
    // (the summary budget derives from it), so don't offer them.
    return (model.config?.context_window ?? 0) > 0
  })
}
