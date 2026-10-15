import { useQueryCache } from '@pinia/colada'
import { useI18n } from 'vue-i18n'
import { toast } from '@felinic/ui'
import { postProvidersByIdImportModels } from '@memohai/sdk'
import type { ProvidersCatalogModel } from '@memohai/sdk'
import { suggestedModelCompatibilities } from '@/constants/client-types'
import { providerSource } from '@/constants/model-sources'
import { catalogModelsFor } from '@/lib/models-dev'
import { useLoadModelsDevCatalog } from './useModelsDevCatalog'

const MODEL_QUERY_KEYS = ['provider-models', 'models'] as const

interface ImportableProvider {
  id?: string
  client_type?: string
  metadata?: Record<string, unknown>
}

export function useProviderModelCatalog() {
  const queryCache = useQueryCache()
  const loadCatalog = useLoadModelsDevCatalog()
  const { t } = useI18n()

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
  // fromEndpoint asks for it. A provider with a source never receives the
  // protocol's capability guesses for its endpoint's models: the catalog did
  // not list them, so nothing says what they do.
  async function importProviderModels(provider: ImportableProvider, options: { fromEndpoint?: boolean } = {}) {
    if (!provider.id) throw new Error('provider is missing')
    const source = providerSource(provider)
    if (source && !options.fromEndpoint) {
      const models = await catalogModels(source, provider.client_type ?? '')
      if (models.length) return syncProviderModelCatalog(provider.id, undefined, models)
    }
    return syncProviderModelCatalog(provider.id, source ? undefined : suggestedModelCompatibilities(provider.client_type))
  }

  // catalogModels reads a source's models, or none when models.dev cannot be
  // reached: the import then falls back to the endpoint and says so.
  async function catalogModels(source: string, clientType: string) {
    try {
      return catalogModelsFor(await loadCatalog(), source, clientType)
    } catch {
      toast.warning(t('models.catalogUnavailable'))
      return []
    }
  }

  return {
    syncProviderModelCatalog,
    importProviderModels,
  }
}
