import type { ModelsGetResponse, ProvidertemplatesGetResponse } from '@memohai/sdk'
import type { ProviderPreset } from '@/constants/provider-presets'

export function findProviderTemplate(
  templates: ProvidertemplatesGetResponse[],
  preset: ProviderPreset,
): ProvidertemplatesGetResponse | undefined {
  return templates.find(template =>
    template.domain === 'llm'
    && template.key === preset.id,
  )
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
