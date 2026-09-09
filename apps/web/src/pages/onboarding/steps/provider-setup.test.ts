import { describe, expect, it } from 'vitest'
import type { ModelsGetResponse, ProvidertemplatesGetResponse } from '@memohai/sdk'
import type { ProviderPreset } from '@/constants/provider-presets'
import {
  findProviderTemplate,
  mergeOnboardingModels,
} from './provider-setup'

describe('onboarding provider setup', () => {
  it('matches a preset to an LLM template by stable key, not display name', () => {
    const preset = { id: 'deepseek', name: 'DeepSeek' } as ProviderPreset
    const templates: ProvidertemplatesGetResponse[] = [
      { id: 'wrong-domain', domain: 'speech', key: 'deepseek', name: 'DeepSeek' },
      { id: 'wrong-key', domain: 'llm', key: 'custom', name: 'DeepSeek' },
      { id: 'deepseek-template', domain: 'llm', key: 'deepseek', name: 'Renamed DeepSeek' },
    ]

    expect(findProviderTemplate(templates, preset)?.id).toBe('deepseek-template')
  })

  it('adds disabled onboarding provider models without duplicating enabled models', () => {
    const enabled: ModelsGetResponse[] = [
      { id: 'flash-id', model_id: 'deepseek-v4-flash', type: 'chat', enable: true },
    ]
    const providerModels: ModelsGetResponse[] = [
      { id: 'flash-id', model_id: 'deepseek-v4-flash', type: 'chat', enable: false },
      { id: 'pro-id', model_id: 'deepseek-v4-pro', type: 'chat', enable: false },
    ]

    expect(mergeOnboardingModels(enabled, providerModels).map(model => ({
      id: model.id,
      enable: model.enable,
    }))).toEqual([
      { id: 'flash-id', enable: true },
      { id: 'pro-id', enable: false },
    ])
  })
})
