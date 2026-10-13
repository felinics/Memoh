import { describe, expect, it } from 'vitest'
import {
  buildModelConfig,
  capabilityFormFrom,
  defaultCapabilityForm,
  defaultReasoningForm,
  reasoningFormFrom,
  reasoningIsUncontrollable,
} from './model-config'

describe('buildModelConfig', () => {
  it('writes a text-only chat model with no reasoning', () => {
    expect(buildModelConfig({
      type: 'chat',
      description: '   ',
      capabilities: { ...defaultCapabilityForm(), toolCall: true },
      reasoning: defaultReasoningForm(),
    })).toEqual({
      schema_version: 2,
      modalities: { input: ['text'], output: ['text'] },
      tool_call: true,
    })
  })

  it('orders modalities and always keeps text input', () => {
    const config = buildModelConfig({
      type: 'chat',
      description: '  General purpose model.  ',
      capabilities: { input: ['pdf', 'image'], output: ['image', 'text'], toolCall: false, structuredOutput: true },
      reasoning: defaultReasoningForm(),
    })
    expect(config.description).toBe('General purpose model.')
    expect(config.modalities).toEqual({ input: ['text', 'image', 'pdf'], output: ['text', 'image'] })
    expect(config.structured_output).toBe(true)
    expect(config.tool_call).toBeUndefined()
  })

  it('writes every reasoning field the form holds', () => {
    const config = buildModelConfig({
      type: 'chat',
      capabilities: defaultCapabilityForm(),
      reasoning: {
        enabled: true,
        efforts: ['high', 'low'],
        toggle: true,
        dialect: 'budget',
        off: 'accepted',
        defaultEnabled: 'off',
        defaultEffort: 'high',
        budgetMin: 1024,
        budgetMax: 32000,
        replayField: 'reasoning_content',
        replayRequired: true,
      },
    })
    expect(config.reasoning).toEqual({
      efforts: ['low', 'high'],
      toggle: true,
      dialect: 'budget',
      off: 'accepted',
      budget: { min: 1024, max: 32000 },
      default: { enabled: false, effort: 'high' },
      replay: { field: 'reasoning_content', required: true },
    })
  })

  it('drops a default effort the model no longer offers', () => {
    const config = buildModelConfig({
      type: 'chat',
      capabilities: defaultCapabilityForm(),
      reasoning: { ...defaultReasoningForm(), enabled: true, efforts: ['low'], defaultEffort: 'max' },
    })
    expect(config.reasoning).toEqual({ efforts: ['low'] })
  })

  it('keeps unrelated stored keys and clears what the form turned off', () => {
    expect(buildModelConfig({
      type: 'chat',
      description: '   ',
      contextWindow: 128000,
      maxOutputTokens: 8192,
      capabilities: defaultCapabilityForm(),
      reasoning: defaultReasoningForm(),
      existing: {
        schema_version: 2,
        description: 'Old description',
        catalog_available: true,
        tool_call: true,
        reasoning: { efforts: ['low'] },
      },
    })).toEqual({
      schema_version: 2,
      description: '',
      catalog_available: true,
      modalities: { input: ['text'], output: ['text'] },
      context_window: 128000,
      max_output_tokens: 8192,
    })
  })

  it('writes only dimensions for an embedding model', () => {
    expect(buildModelConfig({
      type: 'embedding',
      dimensions: 1536,
      capabilities: { ...defaultCapabilityForm(), toolCall: true },
      reasoning: { ...defaultReasoningForm(), enabled: true },
      existing: { schema_version: 2, tool_call: true, modalities: { input: ['text'] } },
    })).toEqual({ schema_version: 2, description: '', dimensions: 1536 })
  })
})

describe('model config form state', () => {
  it('round-trips a stored config through the form', () => {
    const stored = {
      schema_version: 2,
      modalities: { input: ['text', 'image'] as const, output: ['text'] as const },
      tool_call: true,
      reasoning: {
        efforts: ['low', 'high'],
        dialect: 'adaptive',
        off: 'rejected',
        default: { enabled: true },
        replay: { field: 'reasoning_details' },
      },
    }
    const config = {
      ...stored,
      modalities: { input: [...stored.modalities.input], output: [...stored.modalities.output] },
    }
    expect(buildModelConfig({
      type: 'chat',
      capabilities: capabilityFormFrom(config),
      reasoning: reasoningFormFrom(config),
      existing: config,
    })).toEqual({ ...config, description: '' })
  })

  it('flags reasoning that no request can steer', () => {
    expect(reasoningIsUncontrollable({ ...defaultReasoningForm(), enabled: true, efforts: [] })).toBe(true)
    expect(reasoningIsUncontrollable({ ...defaultReasoningForm(), enabled: true, efforts: [], toggle: true })).toBe(false)
    expect(reasoningIsUncontrollable({ ...defaultReasoningForm(), enabled: true })).toBe(false)
  })
})
