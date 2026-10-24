import { useQuery } from '@pinia/colada'
import { getProvidersCatalogModels } from '@memohai/sdk'

// The browser caches only the server's normalized preview. Source validation,
// upstream freshness and protocol conversion are owned by the server.
export function useModelsDevCatalog(
  source: () => string | undefined,
  clientType: () => string,
  enabled: () => boolean = () => true,
) {
  return useQuery({
    key: () => ['models-dev-catalog', source() ?? '', clientType()],
    query: async ({ signal }) => {
      const { data } = await getProvidersCatalogModels({
        query: { source: source() ?? '', client_type: clientType() },
        signal,
        throwOnError: true,
      })
      return data
    },
    enabled: () => enabled() && !!source() && !!clientType(),
    staleTime: 30_000,
  })
}
