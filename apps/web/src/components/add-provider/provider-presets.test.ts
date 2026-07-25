import { describe, expect, it } from 'vitest'
import { onboardingProviderPresets, providerPresets } from '@/constants/provider-presets'
import { suggestProviderName } from './provider-presets'

describe('provider preset helpers', () => {
  it('keeps onboarding presets focused while settings can use the full preset catalog', () => {
    const catalogIds = new Set(providerPresets.map(preset => preset.id))
    const onboardingIds = onboardingProviderPresets.map(preset => preset.id)

    // Onboarding stays a non-empty strict subset: self-hosted and OAuth-only
    // providers belong in settings, not the first-run flow.
    expect(onboardingIds.length).toBeGreaterThan(0)
    expect(onboardingIds.length).toBeLessThan(providerPresets.length)
    for (const settingsOnly of ['ollama', 'github-copilot']) {
      expect(catalogIds.has(settingsOnly)).toBe(true)
      expect(onboardingIds).not.toContain(settingsOnly)
    }
  })

  it('keeps registry source metadata separate from provider instances', () => {
    const deepseek = providerPresets.find(preset => preset.id === 'deepseek')
    const zai = providerPresets.find(preset => preset.id === 'zai')
    const perplexity = providerPresets.find(preset => preset.id === 'perplexity')
    const cerebras = providerPresets.find(preset => preset.id === 'cerebras')

    expect(deepseek?.source).toBe('deepseek.yaml')
    expect(zai?.source).toBe('zai.yaml')
    expect(perplexity?.source).toBe('perplexity.yaml')
    expect(perplexity?.icon).toBe('perplexity-color')
    expect(cerebras?.source).toBe('cerebras.yaml')
    expect(cerebras?.icon).toBe('cerebras-color')
  })

  it('suggests a unique provider instance name for repeat preset accounts', () => {
    expect(suggestProviderName('DeepSeek', [
      { name: 'OpenAI' },
      { name: 'DeepSeek' },
      { name: 'DeepSeek 2' },
    ])).toBe('DeepSeek 3')
  })
})
