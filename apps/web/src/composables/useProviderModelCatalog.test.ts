import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  post: vi.fn(),
  catalogPost: vi.fn(),
  invalidate: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@pinia/colada', () => ({
  useQueryCache: () => ({ invalidateQueries: mocks.invalidate }),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@felinic/ui', () => ({ toast: { warning: mocks.warning } }))
vi.mock('@memohai/sdk', () => ({ postProvidersByIdImportModels: mocks.post, postProvidersByIdImportCatalogModels: mocks.catalogPost }))

import { useProviderModelCatalog } from './useProviderModelCatalog'

describe('server catalog imports', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.post.mockResolvedValue({ data: { created: 1, updated: 0, skipped: 0 } })
    mocks.catalogPost.mockResolvedValue({ data: { created: 1, updated: 0, skipped: 0 } })
  })

  it('asks the server to resolve the source, without sending client-derived models', async () => {
    await useProviderModelCatalog().importProviderModels({
      id: 'p', client_type: 'openai-completions', metadata: { source: 'deepinfra' },
    }, { skipModelIds: ['manually-added'] })
    expect(mocks.catalogPost).toHaveBeenCalledWith({
      path: { id: 'p' },
      body: { skip_model_ids: ['manually-added'] },
      throwOnError: true,
    })
    expect(mocks.invalidate).toHaveBeenCalledWith({ key: ['provider-models'] })
  })

  it('retains explicit endpoint imports without guessing capabilities for a catalog provider', async () => {
    await useProviderModelCatalog().importProviderModels({
      id: 'p', client_type: 'openai-completions', metadata: { source: 'deepinfra' },
    }, { fromEndpoint: true })
    expect(mocks.post.mock.calls[0]![0].body).toBeUndefined()
  })

  it('retains endpoint defaults for providers without a public source', async () => {
    await useProviderModelCatalog().importProviderModels({
      id: 'p', client_type: 'openai-completions', metadata: {},
    })
    expect(mocks.post.mock.calls[0]![0].body).toEqual({ default_compatibilities: ['tool-call', 'reasoning'] })
  })

  it('does not turn a failed catalog import into a different endpoint import', async () => {
    const error = { code: 'provider.catalog_unavailable' }
    mocks.catalogPost.mockRejectedValue(error)
    await expect(useProviderModelCatalog().importProviderModels({
      id: 'p', metadata: { source: 'deepseek' },
    })).rejects.toBe(error)
    expect(mocks.catalogPost).toHaveBeenCalledTimes(1)
    expect(mocks.post).not.toHaveBeenCalled()
  })

  it('discloses use of stale server metadata', async () => {
    mocks.catalogPost.mockResolvedValue({ data: { created: 0, catalog: { stale: true } } })
    await useProviderModelCatalog().importProviderModels({ id: 'p', metadata: { source: 'deepseek' } })
    expect(mocks.warning).toHaveBeenCalledWith('models.catalogStale')
  })
})
