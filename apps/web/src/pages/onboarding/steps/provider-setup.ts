import type { ModelsGetResponse } from '@memohai/sdk'
import type { ProviderPreset } from '@/constants/provider-presets'
import { findModelSource, TEMPLATE_SOURCES, type ModelSource } from '@/constants/model-sources'

// The gallery entry an onboarding preset stands for. Presets are keyed like the
// former provider templates.
export function findPresetEntry(preset: ProviderPreset): ModelSource | undefined {
  return findModelSource(TEMPLATE_SOURCES[preset.id] ?? preset.id)
}

export function mergeOnboardingModels(
  enabledModels: ModelsGetResponse[],
  providerModels: ModelsGetResponse[],
): ModelsGetResponse[] {
  const merged = new Map<string, ModelsGetResponse>()
  for (const model of [...enabledModels, ...providerModels]) {
    const key = model.id || `${model.provider_id ?? ''}\u0000${model.model_id ?? ''}`
    if (!key) continue
    const existing = merged.get(key)
    merged.set(key, existing
      ? { ...existing, ...model, enable: !!existing.enable || !!model.enable }
      : model)
  }
  return [...merged.values()]
}
