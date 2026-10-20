import type { ModelsModality, ModelsModelConfig, ModelsModelReasoning } from '@memohai/sdk'

// The models.dev catalog the provider gallery previews and imports from. It is
// requested by the browser directly; models.dev allows any origin.
export const MODELS_DEV_CATALOG_URL = 'https://models.dev/api.json'

export interface ModelsDevReasoningOption {
  type: 'toggle' | 'effort' | 'budget_tokens' | string
  // Effort values; the catalog lists null for some models.
  values?: (string | null)[]
  min?: number
  max?: number
}

export interface ModelsDevModel {
  id: string
  name?: string
  description?: string
  type?: string
  reasoning?: boolean
  reasoning_options?: ModelsDevReasoningOption[] | null
  interleaved?: true | { field?: string }
  tool_call?: boolean
  structured_output?: boolean
  modalities?: { input?: string[], output?: string[] }
  limit?: { context?: number, output?: number }
  status?: string
  // Request shape for this model when it differs from its provider's.
  provider?: { npm?: string, api?: string, shape?: string }
}

export interface ModelsDevProvider {
  id: string
  name?: string
  npm?: string
  models?: Record<string, ModelsDevModel>
}

export type ModelsDevCatalog = Record<string, ModelsDevProvider>

export async function fetchModelsDevCatalog(signal?: AbortSignal): Promise<ModelsDevCatalog> {
  const response = await fetch(MODELS_DEV_CATALOG_URL, { signal })
  if (!response.ok) throw new Error(`models.dev answered ${response.status}`)
  return await response.json() as ModelsDevCatalog
}

// A model as Memoh imports it: the catalog row turned into a model config.
export interface CatalogModel {
  model_id: string
  // Absent when models.dev gives no name other than the id.
  name?: string
  type: 'chat'
  config: ModelsModelConfig
}

// The chat models a models.dev provider lists, as Memoh would store them for a
// provider of clientType. Embedding models are left out, by type or by id: the
// catalog does not carry their vector size, which Memoh needs to store one,
// and it marks almost none of them with a type.
export function catalogModelsFor(catalog: ModelsDevCatalog | undefined, source: string | undefined, clientType: string): CatalogModel[] {
  const provider = source ? catalog?.[source] : undefined
  if (!provider?.models) return []
  return Object.values(provider.models)
    .filter(model => model.id && (!model.type || model.type === 'chat') && model.status !== 'deprecated' && !looksLikeEmbeddingModel(model.id))
    .map((model) => {
      // A model without a name of its own is stored without one, so the list
      // does not show its id twice.
      const name = model.name?.trim()
      return {
        model_id: model.id,
        ...(name && name !== model.id && { name }),
        type: 'chat' as const,
        config: modelConfigFromCatalog(model, modelProtocol(model, provider, clientType)),
      }
    })
    .sort((a, b) => (a.name ?? a.model_id).localeCompare(b.name ?? b.model_id))
}

// An id in which "embed" starts a word names an embedding model, by the same
// rule the server applies to a provider's own model list.
const EMBEDDING_MODEL_ID = /(?:^|[^a-z])embed/i

function looksLikeEmbeddingModel(modelID: string): boolean {
  return EMBEDDING_MODEL_ID.test(modelID)
}

const NPM_PROTOCOLS: Record<string, string> = {
  '@ai-sdk/openai': 'openai-responses',
  '@ai-sdk/anthropic': 'anthropic-messages',
  '@ai-sdk/google': 'google-generative-ai',
  '@ai-sdk/openai-compatible': 'openai-completions',
}

// The protocol a model's reasoning request uses. Providers that route each model
// to its own protocol (OpenCode Go, GitHub Copilot) name it on the model; every
// other provider speaks its client type.
function modelProtocol(model: ModelsDevModel, provider: ModelsDevProvider, clientType: string): string {
  if (clientType !== 'opencode-go' && clientType !== 'github-copilot') return clientType
  const shape = model.provider?.shape
  if (shape === 'responses') return 'openai-responses'
  if (shape === 'completions') return 'openai-completions'
  return NPM_PROTOCOLS[model.provider?.npm ?? provider.npm ?? ''] ?? 'openai-completions'
}

const MODALITIES = new Set<ModelsModality>(['text', 'image', 'audio', 'video', 'pdf'])

function modalities(values: string[] | undefined, fallback: ModelsModality[]): ModelsModality[] {
  const known = (values ?? []).filter((value): value is ModelsModality => MODALITIES.has(value as ModelsModality))
  return known.length ? known : fallback
}

export function modelConfigFromCatalog(model: ModelsDevModel, protocol: string): ModelsModelConfig {
  const config: ModelsModelConfig = {
    schema_version: 2,
    modalities: {
      input: modalities(model.modalities?.input, ['text']),
      output: modalities(model.modalities?.output, ['text']),
    },
  }
  const description = model.description?.trim()
  if (description) config.description = description
  if (model.tool_call) config.tool_call = true
  if (model.structured_output) config.structured_output = true
  if (model.limit?.context && model.limit.context > 0) config.context_window = model.limit.context
  if (model.limit?.output && model.limit.output > 0) config.max_output_tokens = model.limit.output
  const reasoning = reasoningFromCatalog(model, protocol)
  if (reasoning) config.reasoning = reasoning
  return config
}

// reasoningFromCatalog reads a model's reasoning controls from its
// reasoning_options. The request shape follows from which controls the model
// offers on its protocol: effort levels are tiers, a budget alone is the budget
// shape, and on Anthropic effort without a budget is adaptive thinking. These
// rules reproduce every dialect and off declaration the former provider
// templates made by hand.
export function reasoningFromCatalog(model: ModelsDevModel, protocol: string): ModelsModelReasoning | undefined {
  if (!model.reasoning) return undefined
  const options = model.reasoning_options
  // Reasoning with no declared controls is served the common tiers.
  if (options == null) return withReplay({ efforts: ['low', 'medium', 'high'] }, model)
  // An empty list declares a model that always reasons and takes no control.
  if (options.length === 0) return withReplay({}, model)

  let efforts: string[] = []
  let toggle = false
  let budget: { min?: number, max?: number } | undefined
  for (const option of options) {
    if (option.type === 'effort') efforts = (option.values ?? []).filter((value): value is string => typeof value === 'string' && value !== '')
    else if (option.type === 'toggle') toggle = true
    // models.dev writes a dynamic budget as -1, which no bound can express.
    else if (option.type === 'budget_tokens') budget = { min: atLeast(option.min, 0), max: atLeast(option.max, 1) }
  }
  // OpenAI spells off as the effort "none"; Memoh declares it as a toggle.
  const offByEffort = efforts.includes('none')
  efforts = efforts.filter(effort => effort !== 'none' && effort !== 'default')

  const reasoning: ModelsModelReasoning = {}
  if (efforts.length) reasoning.efforts = efforts
  const bounds = budget && (budget.min !== undefined || budget.max !== undefined)
    ? { ...(budget.min !== undefined && { min: budget.min }), ...(budget.max !== undefined && { max: budget.max }) }
    : undefined
  if (bounds) reasoning.budget = bounds

  // Claude's rules are Claude's: another model behind an Anthropic Messages
  // endpoint (MiniMax, Kimi) is read like any other protocol's.
  if (protocol === 'anthropic-messages' && claudeVersion(model.id)) {
    if (efforts.length && !budget) reasoning.dialect = 'adaptive'
    else if (budget && !efforts.length) reasoning.dialect = 'budget'
    else if (efforts.length && budget) reasoning.dialect = claudeReadsAdaptive(model.id) ? 'adaptive' : 'budget'
    if (toggle) {
      reasoning.toggle = true
      reasoning.off = 'accepted'
    } else if (isClaude4(model.id) || !claudeReadsAdaptive(model.id)) {
      // Claude 4 and older models on this protocol think only when asked, so
      // they can always be turned off.
      reasoning.off = 'accepted'
    }
  } else if (protocol === 'google-generative-ai') {
    // The server sends Gemini thinking as a budget or a level. A model that
    // only offers a switch (Gemma 4) gets no control at all: declaring a
    // toggle the server cannot send would offer a control that never reaches
    // the model.
    reasoning.dialect = efforts.length ? 'tier' : budget ? 'budget' : ''
    // Gemini 2.5 turns off with a zero budget; otherwise it cannot.
    const off = budget?.min === 0
    if (off) reasoning.toggle = true
    reasoning.off = off ? 'accepted' : 'rejected'
  } else {
    reasoning.dialect = efforts.length ? 'tier' : budget ? 'budget' : toggle ? 'toggle' : ''
    if (toggle || offByEffort) reasoning.toggle = true
    if (toggle) reasoning.off = 'accepted'
  }
  if (!reasoning.dialect) delete reasoning.dialect
  return withReplay(reasoning, model)
}

// DeepSeek and Kimi reject a thinking-mode tool call replayed without its
// reasoning_content, so their replay is required.
function withReplay(reasoning: ModelsModelReasoning, model: ModelsDevModel): ModelsModelReasoning {
  const interleaved = model.interleaved
  const field = typeof interleaved === 'object' ? interleaved.field : undefined
  if (field === 'reasoning_content' || field === 'reasoning_details') {
    reasoning.replay = { field }
    if (field === 'reasoning_content' && /deepseek|kimi/i.test(model.id)) reasoning.replay.required = true
  }
  return reasoning
}

const CLAUDE_VERSION = /claude-(?:[a-z]+-)?(\d+)(?:[.-](\d{1,2}))?(?:[.:@-]|$)/

function atLeast(value: number | undefined, floor: number): number | undefined {
  return typeof value === 'number' && value >= floor ? value : undefined
}

function claudeVersion(modelID: string): [number, number] | null {
  const id = modelID.toLowerCase()
  if (!id.includes('claude')) return null
  const match = CLAUDE_VERSION.exec(id)
  if (!match) return [Infinity, 0]
  return [Number(match[1]), Number(match[2] ?? 0)]
}

// Claude 4.6 and later use adaptive thinking; an unparsable Claude id reads as
// new, as the server's reasoning package does.
function claudeReadsAdaptive(modelID: string): boolean {
  const version = claudeVersion(modelID)
  if (!version) return false
  const [major, minor] = version
  return major >= 5 || (major === 4 && minor >= 6)
}

function isClaude4(modelID: string): boolean {
  return claudeVersion(modelID)?.[0] === 4
}
