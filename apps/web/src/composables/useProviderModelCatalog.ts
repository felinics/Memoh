import { useQueryCache } from '@pinia/colada'
import { postProvidersByIdImportModels } from '@memohai/sdk'

const MODEL_QUERY_KEYS = ['provider-models', 'models'] as const

export function useProviderModelCatalog() {
  const queryCache = useQueryCache()

  async function syncProviderModelCatalog(providerId: string) {
    const { data } = await postProvidersByIdImportModels({
      path: { id: providerId },
      throwOnError: true,
    })
    for (const key of MODEL_QUERY_KEYS) {
      queryCache.invalidateQueries({ key: [key] })
    }
    return data
  }

  return {
    syncProviderModelCatalog,
  }
}
