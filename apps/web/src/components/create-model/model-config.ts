import type { ModelsModelConfig } from '@memohai/sdk'

// Tokens a model may declare in reasoning_efforts, in the order the server ranks
// them (internal/reasoning: "disable" first, then tiers weakest to strongest).
// "disable" declares that the model can be turned off; it is not a tier.
export const DECLARABLE_EFFORTS = ['disable', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'] as const

interface BuildModelConfigInput {
  type: string
  description?: string
  dimensions?: number
  contextWindow?: number
  maxOutputTokens?: number
  compatibilities: string[]
  reasoningEfforts?: string[]
  existing?: ModelsModelConfig
}

export function buildModelConfig(input: BuildModelConfigInput): ModelsModelConfig {
  const config: ModelsModelConfig = { ...(input.existing ?? {}) }
  const description = input.description?.trim() ?? ''

  if (input.existing) config.description = description
  else if (description) config.description = description
  else delete config.description

  if (input.type === 'embedding') {
    config.dimensions = input.dimensions
    delete config.compatibilities
    delete config.context_window
    delete config.max_output_tokens
    delete config.reasoning_efforts
    delete config.thinking_mode
    return config
  }

  delete config.dimensions
  config.compatibilities = input.compatibilities
  if (input.contextWindow) config.context_window = input.contextWindow
  else delete config.context_window
  if (input.maxOutputTokens) config.max_output_tokens = input.maxOutputTokens
  else delete config.max_output_tokens
  applyReasoning(config, input.compatibilities.includes('reasoning') ? input.reasoningEfforts : undefined)
  return config
}

// applyReasoning writes the declared effort list. thinking_mode stays as the
// catalog left it (or undeclared, so the server infers it from the model id) —
// except when reasoning is unchecked: a declared mode outranks the compatibility
// flag on the server, so leaving an imported mode behind would keep the model
// thinking after the user turned reasoning off. Catalog-only fields
// (reasoning_dialect, reasoning_off_support, budgets) are kept as they came.
function applyReasoning(config: ModelsModelConfig, efforts?: string[]) {
  if (!efforts) {
    delete config.thinking_mode
    delete config.reasoning_efforts
    return
  }
  // An empty list means "unknown", which the server serves as low/medium/high.
  const ordered = DECLARABLE_EFFORTS.filter(e => efforts.includes(e))
  if (ordered.length) config.reasoning_efforts = ordered
  else delete config.reasoning_efforts
}
