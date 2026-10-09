import { describe, expect, it } from 'vitest'
import { buildModelConfig } from './model-config'

describe('buildModelConfig', () => {
  it('omits an empty description for a new model', () => {
    expect(buildModelConfig({
      type: 'chat',
      description: '   ',
      compatibilities: ['tool-call'],
    })).toEqual({ compatibilities: ['tool-call'] })
  })

  it('trims a new description', () => {
    expect(buildModelConfig({
      type: 'chat',
      description: '  General purpose model.  ',
      compatibilities: [],
    })).toEqual({
      description: 'General purpose model.',
      compatibilities: [],
    })
  })

  it('preserves unknown config and explicit clearing while editing', () => {
    expect(buildModelConfig({
      type: 'chat',
      description: '   ',
      compatibilities: ['vision', 'reasoning'],
      contextWindow: 128000,
      maxOutputTokens: 8192,
      reasoningEfforts: ['high', 'low'],
      existing: {
        description: 'Old description',
        thinking_mode: 'adaptive',
        reasoning_efforts: ['low', 'high'],
      },
    })).toEqual({
      description: '',
      compatibilities: ['vision', 'reasoning'],
      context_window: 128000,
      max_output_tokens: 8192,
      thinking_mode: 'adaptive',
      reasoning_efforts: ['low', 'high'],
    })
  })
})
