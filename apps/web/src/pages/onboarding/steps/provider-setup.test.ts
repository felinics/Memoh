import { describe, expect, it } from 'vitest'
import type { ModelsGetResponse } from '@memohai/sdk'
import type { ProviderPreset } from '@/constants/provider-presets'
import {
  findPresetEntry,
  mergeOnboardingModels,
} from './provider-setup'

describe('onboarding provider setup', () => {
  it('matches a preset to a gallery entry by its stable key', () => {
    expect(findPresetEntry({ id: 'deepseek', name: 'Renamed' } as ProviderPreset)?.id).toBe('deepseek')
    expect(findPresetEntry({ id: 'qwen', name: 'Qwen' } as ProviderPreset)?.id).toBe('alibaba-cn')
    expect(findPresetEntry({ id: 'nobody', name: 'Nobody' } as ProviderPreset)).toBeUndefined()
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
