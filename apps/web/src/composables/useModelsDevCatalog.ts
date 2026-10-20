import { defineQueryOptions, useQuery, useQueryCache } from '@pinia/colada'
import { fetchModelsDevCatalog, type ModelsDevCatalog } from '@/lib/models-dev'

// The models.dev catalog, shared by the gallery preview and imports through
// the one query cache: one request per half hour, and a failed request is not
// kept.
export const modelsDevCatalogQuery = defineQueryOptions({
  key: ['models-dev-catalog'],
  // A superseded request is aborted rather than left to download and parse.
  query: ({ signal }) => fetchModelsDevCatalog(signal),
  staleTime: 30 * 60 * 1000,
})

// The models.dev catalog, for views that preview a provider's models.
export function useModelsDevCatalog(enabled: () => boolean = () => true) {
  return useQuery({ ...modelsDevCatalogQuery, enabled })
}

// useLoadModelsDevCatalog reads the catalog on demand through the same cache,
// for an import that runs outside a view's query.
export function useLoadModelsDevCatalog(): () => Promise<ModelsDevCatalog> {
  const queryCache = useQueryCache()
  return async () => {
    const state = await queryCache.refresh(queryCache.ensure(modelsDevCatalogQuery))
    if (state.status !== 'success') throw state.error ?? new Error('models.dev catalog is not loaded')
    return state.data
  }
}
