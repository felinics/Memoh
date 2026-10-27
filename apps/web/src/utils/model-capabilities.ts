import type { ModelsModality, ModelsModelConfig } from '@memohai/sdk'

type ModelConfigLike = Pick<ModelsModelConfig, 'modalities' | 'tool_call'> | null | undefined

// Whether the model takes the modality as input, per its stored config.
export function modelAccepts(config: ModelConfigLike, modality: ModelsModality): boolean {
  return !!config?.modalities?.input?.includes(modality)
}

// Whether the model returns the modality as output, per its stored config.
export function modelProduces(config: ModelConfigLike, modality: ModelsModality): boolean {
  return !!config?.modalities?.output?.includes(modality)
}
