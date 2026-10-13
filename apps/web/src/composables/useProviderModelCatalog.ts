import { useQueryCache } from '@pinia/colada'
import { postProvidersByIdImportModels } from '@memohai/sdk'
import type { ProvidersCatalogModel } from '@memohai/sdk'
import { suggestedModelCompatibilities } from '@/constants/client-types'
import { providerSource } from '@/constants/model-sources'
import { catalogModelsFor, loadModelsDevCatalog } from '@/lib/models-dev'

const MODEL_QUERY_KEYS = ['provider-models', 'models'] as const

interface ImportableProvider {
  id?: string
  client_type?: string
  metadata?: Record<string, unknown>
}

export function useProviderModelCatalog() {
  const queryCache = useQueryCache()

  async function syncProviderModelCatalog(
    providerId: string,
    defaultCompatibilities?: string[],
    models?: ProvidersCatalogModel[],
  ) {
    const body = models?.length
      ? { models }
      : defaultCompatibilities ? { default_compatibilities: defaultCompatibilities } : undefined
    const { data } = await postProvidersByIdImportModels({
      path: { id: providerId },
      ...(body && { body }),
      throwOnError: true,
    })
    for (const key of MODEL_QUERY_KEYS) {
      queryCache.invalidateQueries({ key: [key] })
    }
    return data
  }

  // importProviderModels imports a provider's models from its models.dev
  // source when it has one, and from its own /models endpoint otherwise or when
  // fromEndpoint asks for it.
  async function importProviderModels(provider: ImportableProvider, options: { fromEndpoint?: boolean } = {}) {
    if (!provider.id) throw new Error('provider is missing')
    const source = providerSource(provider)
    if (source && !options.fromEndpoint) {
      const catalog = await loadModelsDevCatalog()
      const models = catalogModelsFor(catalog, source, provider.client_type ?? '')
      if (models.length) return syncProviderModelCatalog(provider.id, undefined, models)
    }
    return syncProviderModelCatalog(provider.id, suggestedModelCompatibilities(provider.client_type))
  }

  return {
    syncProviderModelCatalog,
    importProviderModels,
  }
}
