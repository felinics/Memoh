import { useQueryCache } from '@pinia/colada'
import { useI18n } from 'vue-i18n'
import { toast } from '@felinic/ui'
import { postProvidersByIdImportCatalogModels, postProvidersByIdImportModels } from '@memohai/sdk'
import { suggestedModelCompatibilities } from '@/constants/client-types'
import { providerSource } from '@/constants/model-sources'

const MODEL_QUERY_KEYS = ['provider-models', 'models'] as const

interface ImportableProvider {
  id?: string
  client_type?: string
  metadata?: Record<string, unknown>
}

export function useProviderModelCatalog() {
  const queryCache = useQueryCache()
  const { t } = useI18n()

  async function syncProviderModelCatalog(
    providerId: string,
    defaultCompatibilities?: string[],
    catalogOptions?: { skipModelIds?: string[] },
  ) {
    const { data } = catalogOptions
      ? await postProvidersByIdImportCatalogModels({
          path: { id: providerId },
          body: { skip_model_ids: catalogOptions.skipModelIds },
          throwOnError: true,
        })
      : await postProvidersByIdImportModels({
          path: { id: providerId },
          ...(defaultCompatibilities && { body: { default_compatibilities: defaultCompatibilities } }),
          throwOnError: true,
        })
    for (const key of MODEL_QUERY_KEYS) {
      queryCache.invalidateQueries({ key: [key] })
    }
    if (data?.catalog?.stale) toast.warning(t('models.catalogStale'))
    return data
  }

  // The server resolves the catalog source and protocol from the stored
  // provider. An explicit catalog import never silently changes its source.
  // skipModelIds leaves those catalog models out, for models the caller has
  // just created and whose settings the catalog must not overwrite.
  async function importProviderModels(provider: ImportableProvider, options: { fromEndpoint?: boolean, skipModelIds?: string[] } = {}) {
    if (!provider.id) throw new Error('provider is missing')
    const source = providerSource(provider)
    if (source && !options.fromEndpoint) {
      return syncProviderModelCatalog(provider.id, undefined, { skipModelIds: options.skipModelIds })
    }
    return syncProviderModelCatalog(provider.id, source ? undefined : suggestedModelCompatibilities(provider.client_type))
  }

  return {
    syncProviderModelCatalog,
    importProviderModels,
  }
}
