import type { ModelsModality, ModelsModelConfig, ModelsModelReasoning } from '@memohai/sdk'

// The model config schema this form writes. The server reads older versions
// and stores this one on the next save.
export const MODEL_CONFIG_SCHEMA_VERSION = 2

// Effort levels a model may accept, weakest to strongest. Turning reasoning off
// is the toggle and the off answer, not an effort.
export const REASONING_EFFORTS = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max'] as const

export const INPUT_MODALITIES: ModelsModality[] = ['text', 'image', 'audio', 'video', 'pdf']
export const OUTPUT_MODALITIES: ModelsModality[] = ['text', 'image', 'audio']

export const REASONING_DIALECTS = ['tier', 'budget', 'toggle', 'adaptive'] as const
export const REASONING_OFF_SUPPORT = ['accepted', 'low_effort_only', 'rejected'] as const
export const REASONING_REPLAY_FIELDS = ['reasoning_content', 'reasoning_details', 'reasoning'] as const

// What the form edits for reasoning. Empty strings mean "not stated".
export interface ReasoningForm {
  enabled: boolean
  efforts: string[]
  toggle: boolean
  dialect: string
  off: string
  defaultEnabled: '' | 'on' | 'off'
  defaultEffort: string
  budgetMin?: number
  budgetMax?: number
  replayField: string
  replayRequired: boolean
}

export interface CapabilityForm {
  input: ModelsModality[]
  output: ModelsModality[]
  toolCall: boolean
  structuredOutput: boolean
}

// A hand-added model starts as text in, text out, and reasons at the three
// common levels once reasoning is switched on.
export function defaultReasoningForm(): ReasoningForm {
  return {
    enabled: false,
    efforts: ['low', 'medium', 'high'],
    toggle: false,
    dialect: '',
    off: '',
    defaultEnabled: '',
    defaultEffort: '',
    budgetMin: undefined,
    budgetMax: undefined,
    replayField: '',
    replayRequired: false,
  }
}

export function defaultCapabilityForm(): CapabilityForm {
  return { input: ['text'], output: ['text'], toolCall: false, structuredOutput: false }
}

export function capabilityFormFrom(config?: ModelsModelConfig | null): CapabilityForm {
  if (!config) return defaultCapabilityForm()
  return {
    input: [...(config.modalities?.input ?? ['text'])],
    output: [...(config.modalities?.output ?? ['text'])],
    toolCall: !!config.tool_call,
    structuredOutput: !!config.structured_output,
  }
}

export function reasoningFormFrom(config?: ModelsModelConfig | null): ReasoningForm {
  const r = config?.reasoning
  if (!r) return defaultReasoningForm()
  const enabled = r.default?.enabled
  return {
    enabled: true,
    efforts: [...(r.efforts ?? [])],
    toggle: !!r.toggle,
    dialect: r.dialect ?? '',
    off: r.off ?? '',
    defaultEnabled: enabled === undefined ? '' : enabled ? 'on' : 'off',
    defaultEffort: r.default?.effort ?? '',
    budgetMin: r.budget?.min,
    budgetMax: r.budget?.max,
    replayField: r.replay?.field ?? '',
    replayRequired: !!r.replay?.required,
  }
}

interface BuildModelConfigInput {
  type: string
  description?: string
  dimensions?: number
  contextWindow?: number
  maxOutputTokens?: number
  capabilities: CapabilityForm
  reasoning: ReasoningForm
  existing?: ModelsModelConfig
}

export function buildModelConfig(input: BuildModelConfigInput): ModelsModelConfig {
  const config: ModelsModelConfig = { ...(input.existing ?? {}), schema_version: MODEL_CONFIG_SCHEMA_VERSION }
  const description = input.description?.trim() ?? ''

  if (input.existing) config.description = description
  else if (description) config.description = description
  else delete config.description

  if (input.type === 'embedding') {
    config.dimensions = input.dimensions
    delete config.modalities
    delete config.tool_call
    delete config.structured_output
    delete config.reasoning
    delete config.context_window
    delete config.max_output_tokens
    return config
  }

  delete config.dimensions
  config.modalities = {
    input: withUnlisted(ordered(INPUT_MODALITIES, ['text', ...input.capabilities.input]), INPUT_MODALITIES, input.capabilities.input),
    output: withUnlisted(ordered(OUTPUT_MODALITIES, input.capabilities.output), OUTPUT_MODALITIES, input.capabilities.output),
  }
  setFlag(config, 'tool_call', input.capabilities.toolCall)
  setFlag(config, 'structured_output', input.capabilities.structuredOutput)
  if (input.contextWindow) config.context_window = input.contextWindow
  else delete config.context_window
  if (input.maxOutputTokens) config.max_output_tokens = input.maxOutputTokens
  else delete config.max_output_tokens

  const reasoning = buildReasoning(input.reasoning)
  // The form has no field for the default budget, so a saved one is kept.
  const defaultBudget = input.existing?.reasoning?.default?.budget
  if (reasoning && defaultBudget !== undefined) reasoning.default = { ...reasoning.default, budget: defaultBudget }
  if (reasoning) config.reasoning = reasoning
  else delete config.reasoning
  return config
}

function buildReasoning(form: ReasoningForm): ModelsModelReasoning | undefined {
  if (!form.enabled) return undefined
  const r: ModelsModelReasoning = {}
  const efforts: string[] = ordered(REASONING_EFFORTS, form.efforts)
  if (efforts.length) r.efforts = efforts
  if (form.toggle) r.toggle = true
  if (form.dialect) r.dialect = form.dialect
  if (form.off) r.off = form.off
  const min = positiveOrZero(form.budgetMin)
  const max = positiveOrZero(form.budgetMax)
  if (min !== undefined || max !== undefined) r.budget = { min, max }
  const defaults: NonNullable<ModelsModelReasoning['default']> = {}
  if (form.defaultEnabled) defaults.enabled = form.defaultEnabled === 'on'
  if (form.defaultEffort && efforts.includes(form.defaultEffort)) defaults.effort = form.defaultEffort
  if (Object.keys(defaults).length) r.default = defaults
  if (form.replayField) r.replay = { field: form.replayField, ...(form.replayRequired && { required: true }) }
  return r
}

// reasoningBudgetError names why the budget the form holds would be refused by
// the server: a maximum that is not positive, or a minimum above the maximum.
export function reasoningBudgetError(form: ReasoningForm): 'max' | 'range' | undefined {
  if (!form.enabled) return undefined
  const max = typeof form.budgetMax === 'number' && Number.isFinite(form.budgetMax) ? form.budgetMax : undefined
  const min = positiveOrZero(form.budgetMin)
  if (max !== undefined && max <= 0) return 'max'
  if (min !== undefined && max !== undefined && min > max) return 'range'
  return undefined
}

// A model that reasons but takes no effort, toggle or budget cannot be steered;
// the chat picker then shows no control for it.
export function reasoningIsUncontrollable(form: ReasoningForm): boolean {
  return form.enabled && !form.toggle && form.efforts.length === 0
    && positiveOrZero(form.budgetMin) === undefined && positiveOrZero(form.budgetMax) === undefined
}

function ordered<T extends string>(order: readonly T[], values: readonly string[]): T[] {
  return order.filter(value => values.includes(value))
}

// withUnlisted appends the modalities the form has no checkbox for, so saving
// keeps a video or PDF output the server stored rather than dropping it.
function withUnlisted<T extends string>(listed: T[], order: readonly T[], values: readonly T[]): T[] {
  return [...listed, ...values.filter(value => !order.includes(value) && !listed.includes(value))]
}

function setFlag(config: ModelsModelConfig, key: 'tool_call' | 'structured_output', value: boolean) {
  if (value) config[key] = true
  else delete config[key]
}

// A cleared number input holds '' rather than undefined.
function positiveOrZero(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : undefined
}
