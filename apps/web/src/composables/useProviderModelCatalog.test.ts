import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  postProvidersByIdImportModels: vi.fn(),
  loadModelsDevCatalog: vi.fn(),
}))

vi.mock('@pinia/colada', () => ({ useQueryCache: () => ({ invalidateQueries: vi.fn() }) }))
vi.mock('@memohai/sdk', () => ({ postProvidersByIdImportModels: mocks.postProvidersByIdImportModels }))
vi.mock('@/lib/models-dev', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/models-dev')>()),
  loadModelsDevCatalog: mocks.loadModelsDevCatalog,
}))

import { useProviderModelCatalog } from './useProviderModelCatalog'

describe('importProviderModels', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.postProvidersByIdImportModels.mockResolvedValue({ data: { created: 1 } })
    mocks.loadModelsDevCatalog.mockResolvedValue({
      deepinfra: { id: 'deepinfra', models: { 'm-1': { id: 'm-1', name: 'Model One', tool_call: true } } },
    })
  })

  it('imports the catalog of a provider with a source', async () => {
    const { importProviderModels } = useProviderModelCatalog()
    await importProviderModels({ id: 'p', client_type: 'openai-completions', metadata: { source: 'deepinfra' } })
    const body = mocks.postProvidersByIdImportModels.mock.calls[0]![0].body
    expect(body.models).toHaveLength(1)
    expect(body.models[0]).toMatchObject({ model_id: 'm-1', name: 'Model One', type: 'chat', config: { tool_call: true } })
  })

  it('lists the endpoint when asked, or when there is no source', async () => {
    const { importProviderModels } = useProviderModelCatalog()
    await importProviderModels({ id: 'p', client_type: 'openai-completions', metadata: { source: 'deepinfra' } }, { fromEndpoint: true })
    await importProviderModels({ id: 'q', client_type: 'openai-completions', metadata: {} })
    for (const [call] of mocks.postProvidersByIdImportModels.mock.calls) {
      expect(call.body).toEqual({ default_compatibilities: ['tool-call', 'reasoning'] })
    }
    expect(mocks.loadModelsDevCatalog).not.toHaveBeenCalled()
  })

  it('falls back to the endpoint when the catalog has no models for the source', async () => {
    const { importProviderModels } = useProviderModelCatalog()
    await importProviderModels({ id: 'p', client_type: 'openai-completions', metadata: { source: 'nobody' } })
    expect(mocks.postProvidersByIdImportModels.mock.calls[0]![0].body).toEqual({ default_compatibilities: ['tool-call', 'reasoning'] })
  })
})
