import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  postProvidersByIdImportModels: vi.fn(),
  fetchModelsDevCatalog: vi.fn(),
  toastWarning: vi.fn(),
}))

vi.mock('@pinia/colada', () => ({
  defineQueryOptions: (options: unknown) => options,
  useQuery: vi.fn(),
  useQueryCache: () => ({
    invalidateQueries: vi.fn(),
    ensure: (options: { query: (context: { signal: AbortSignal }) => Promise<unknown> }) => options,
    // Pinia Colada hands every query its abort signal.
    refresh: async (entry: { query: (context: { signal: AbortSignal }) => Promise<unknown> }) => {
      try {
        return { status: 'success', data: await entry.query({ signal: new AbortController().signal }) }
      } catch (error) {
        return { status: 'error', error }
      }
    },
  }),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@felinic/ui', () => ({ toast: { warning: mocks.toastWarning } }))
vi.mock('@memohai/sdk', () => ({ postProvidersByIdImportModels: mocks.postProvidersByIdImportModels }))
vi.mock('@/lib/models-dev', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/models-dev')>()),
  fetchModelsDevCatalog: mocks.fetchModelsDevCatalog,
}))

import { useProviderModelCatalog } from './useProviderModelCatalog'

function importBody(call = 0) {
  return mocks.postProvidersByIdImportModels.mock.calls[call]![0].body
}

describe('importProviderModels', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.postProvidersByIdImportModels.mockResolvedValue({ data: { created: 1 } })
    mocks.fetchModelsDevCatalog.mockResolvedValue({
      deepinfra: { id: 'deepinfra', models: { 'm-1': { id: 'm-1', name: 'Model One', tool_call: true } } },
    })
  })

  it('imports the catalog of a provider with a source', async () => {
    const { importProviderModels } = useProviderModelCatalog()
    await importProviderModels({ id: 'p', client_type: 'openai-completions', metadata: { source: 'deepinfra' } })
    expect(importBody().models).toHaveLength(1)
    expect(importBody().models[0]).toMatchObject({ model_id: 'm-1', name: 'Model One', type: 'chat', config: { tool_call: true } })
  })

  // The protocol's capability guesses are for a provider nothing describes; a
  // provider with a source has a catalog, and the models it does not list are
  // unknown rather than assumed.
  it('lists the endpoint without capability guesses when asked for a provider with a source', async () => {
    const { importProviderModels } = useProviderModelCatalog()
    await importProviderModels({ id: 'p', client_type: 'openai-completions', metadata: { source: 'deepinfra' } }, { fromEndpoint: true })
    expect(importBody()).toBeUndefined()
    expect(mocks.fetchModelsDevCatalog).not.toHaveBeenCalled()
  })

  it('lists the endpoint with the protocol defaults when there is no source', async () => {
    const { importProviderModels } = useProviderModelCatalog()
    await importProviderModels({ id: 'q', client_type: 'openai-completions', metadata: {} })
    expect(importBody()).toEqual({ default_compatibilities: ['tool-call', 'reasoning'] })
    expect(mocks.fetchModelsDevCatalog).not.toHaveBeenCalled()
  })

  it('falls back to the endpoint without capability guesses when the catalog has no models for the source', async () => {
    const { importProviderModels } = useProviderModelCatalog()
    await importProviderModels({ id: 'p', client_type: 'openai-completions', metadata: { source: 'nobody' } })
    expect(importBody()).toBeUndefined()
    expect(mocks.toastWarning).not.toHaveBeenCalled()
  })

  it('falls back to the endpoint and says so when models.dev cannot be reached', async () => {
    mocks.fetchModelsDevCatalog.mockRejectedValue(new Error('network'))
    const { importProviderModels } = useProviderModelCatalog()
    await importProviderModels({ id: 'p', client_type: 'openai-completions', metadata: { source: 'deepinfra' } })
    expect(importBody()).toBeUndefined()
    expect(mocks.toastWarning).toHaveBeenCalledWith('models.catalogUnavailable')
  })
})
