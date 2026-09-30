import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  importModels: vi.fn(),
  invalidateQueries: vi.fn(),
}))

vi.mock('@memohai/sdk', () => ({
  postProvidersByIdImportModels: mocks.importModels,
}))

vi.mock('@pinia/colada', () => ({
  useQueryCache: () => ({ invalidateQueries: mocks.invalidateQueries }),
}))

import { useProviderModelCatalog } from './useProviderModelCatalog'

describe('useProviderModelCatalog', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('syncs the provider catalog and invalidates every model picker query', async () => {
    const result = { created: 2, updated: 3, skipped: 1, models: ['one', 'two'] }
    mocks.importModels.mockResolvedValue({ data: result })

    const { syncProviderModelCatalog } = useProviderModelCatalog()

    await expect(syncProviderModelCatalog('provider-id')).resolves.toEqual(result)
    expect(mocks.importModels).toHaveBeenCalledWith({
      path: { id: 'provider-id' },
      throwOnError: true,
    })
    expect(mocks.invalidateQueries.mock.calls).toEqual([
      [{ key: ['provider-models'] }],
      [{ key: ['models'] }],
      [{ key: ['providers', 'provider-id', 'chatgpt-status'] }],
    ])
  })

  it('passes explicit protocol defaults when the caller supplies them', async () => {
    mocks.importModels.mockResolvedValue({ data: { created: 1 } })

    const { syncProviderModelCatalog } = useProviderModelCatalog()
    await syncProviderModelCatalog('provider-id', ['tool-call', 'reasoning'])

    expect(mocks.importModels).toHaveBeenCalledWith({
      path: { id: 'provider-id' },
      body: { default_compatibilities: ['tool-call', 'reasoning'] },
      throwOnError: true,
    })
  })
  it('refreshes authorization state even when catalog import fails', async () => {
    const error = { code: 'chatgpt.not_connected' }
    mocks.importModels.mockRejectedValue(error)
    await expect(useProviderModelCatalog().syncProviderModelCatalog('provider-id')).rejects.toEqual(error)
    expect(mocks.invalidateQueries).toHaveBeenCalledExactlyOnceWith({ key: ['providers', 'provider-id', 'chatgpt-status'] })
  })

})
