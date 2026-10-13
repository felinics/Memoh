import { useQuery } from '@pinia/colada'
import { loadModelsDevCatalog } from '@/lib/models-dev'

// The models.dev catalog, for views that preview a provider's models.
export function useModelsDevCatalog(enabled: () => boolean = () => true) {
  return useQuery({
    key: () => ['models-dev-catalog'],
    query: () => loadModelsDevCatalog(),
    enabled,
    staleTime: 30 * 60 * 1000,
  })
}
