// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h } from 'vue'
import type { ProviderPreset } from '@/constants/provider-presets'

const mocks = vi.hoisted(() => ({
  deleteModel: vi.fn(),
  getProviderModels: vi.fn(),
  getProviderTemplates: vi.fn(),
  getProviderByName: vi.fn(),
  importModels: vi.fn(),
  postProvider: vi.fn(),
  postProviderFromTemplate: vi.fn(),
  putModelsById: vi.fn(),
  testProvider: vi.fn(),
  updateProvider: vi.fn(),
  invalidateQueries: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('@pinia/colada', async () => {
  const { ref } = await import('vue')
  return {
    useMutation: (options: { mutation: (value: never) => Promise<unknown> }) => ({
      mutateAsync: options.mutation,
      isLoading: ref(false),
    }),
    useQuery: (options: { query: () => Promise<unknown> }) => {
      const state = ref({ data: [] })
      return {
        state,
        refresh: async () => {
          state.value.data = await options.query() as never[]
        },
      }
    },
    useQueryCache: () => ({ invalidateQueries: mocks.invalidateQueries }),
  }
})

vi.mock('@memohai/sdk', () => ({
  deleteModelsById: mocks.deleteModel,
  getProviderTemplates: mocks.getProviderTemplates,
  getProvidersByIdModels: mocks.getProviderModels,
  getProvidersNameByName: mocks.getProviderByName,
  postProviders: mocks.postProvider,
  postProvidersByIdImportModels: mocks.importModels,
  postProvidersByIdTest: mocks.testProvider,
  postProvidersFromTemplate: mocks.postProviderFromTemplate,
  putModelsById: mocks.putModelsById,
  putProvidersById: mocks.updateProvider,
}))

import { useProviderSetup } from './useProviderSetup'

const preset: ProviderPreset = {
  id: 'deepseek',
  name: 'DeepSeek',
  clientType: 'openai-completions',
  baseUrl: 'https://api.deepseek.com/v1',
  source: 'deepseek.yaml',
}

function mountSetup(selectedPreset: ProviderPreset | null) {
  let setup: ReturnType<typeof useProviderSetup> | undefined
  const ready = vi.fn()
  const app = createApp({
    setup() {
      setup = useProviderSetup({ selectedPreset: () => selectedPreset, onProviderReady: ready })
      return () => h('div')
    },
  })
  app.mount(document.createElement('div'))
  setup!.initFormValues(selectedPreset)
  return { app, ready, setup: setup! }
}

describe('useProviderSetup', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.testProvider.mockResolvedValue({ data: { status: 'ok' } })
    mocks.importModels.mockResolvedValue({ data: { created: 2 } })
    mocks.getProviderModels.mockResolvedValue({
      data: [
        { id: 'pro-id', model_id: 'deepseek-v4-pro', provider_id: 'provider-id', type: 'chat', enable: false },
        { id: 'flash-id', model_id: 'deepseek-v4-flash', provider_id: 'provider-id', type: 'chat', enable: false },
      ],
    })
  })

  it('hands off a template provider without enabling every imported model', async () => {
    mocks.getProviderTemplates.mockResolvedValue({
      data: [{ id: 'template-id', domain: 'llm', key: 'deepseek' }],
    })
    mocks.postProviderFromTemplate.mockResolvedValue({ data: { id: 'provider-id' } })
    const { app, ready, setup } = mountSetup(preset)
    setup.formValues.value.api_key = 'sk-test'

    await setup.saveAndNext()

    expect(mocks.postProviderFromTemplate).toHaveBeenCalledWith({
      body: {
        template_id: 'template-id',
        domain: 'llm',
        name: 'DeepSeek',
        config: { base_url: preset.baseUrl, api_key: 'sk-test' },
      },
      throwOnError: true,
    })
    expect(mocks.postProvider).not.toHaveBeenCalled()
    expect(mocks.importModels).toHaveBeenCalledWith({
      path: { id: 'provider-id' },
      throwOnError: true,
    })
    expect(ready).toHaveBeenCalledWith({ providerId: 'provider-id' })
    expect(mocks.putModelsById).not.toHaveBeenCalled()
    app.unmount()
  })

  it('never reuses a same-name custom provider for a preset', async () => {
    mocks.getProviderTemplates.mockResolvedValue({
      data: [{ id: 'template-id', domain: 'llm', key: 'deepseek' }],
    })
    mocks.postProviderFromTemplate.mockRejectedValue({ code: 'provider.name_taken' })
    mocks.getProviderByName.mockResolvedValue({ data: { id: 'custom-provider-id' } })
    const { app, ready, setup } = mountSetup(preset)
    setup.formValues.value.api_key = 'sk-test'

    await setup.saveAndNext()

    expect(mocks.updateProvider).not.toHaveBeenCalled()
    expect(ready).not.toHaveBeenCalled()
    app.unmount()
  })

  it.each([
    'openai-responses',
    'openai-completions',
    'anthropic-messages',
    'google-generative-ai',
  ])('imports custom %s providers with explicit protocol defaults', async (clientType) => {
    mocks.getProviderByName.mockResolvedValue({ data: null })
    mocks.postProvider.mockResolvedValue({ data: { id: 'provider-id' } })
    const { app, setup } = mountSetup(null)
    setup.formValues.value = {
      name: 'My Provider',
      api_key: 'sk-test',
      base_url: 'https://example.com/v1',
      client_type: clientType,
    }

    await setup.saveAndNext()

    expect(mocks.postProvider).toHaveBeenCalled()
    expect(mocks.postProviderFromTemplate).not.toHaveBeenCalled()
    expect(mocks.importModels).toHaveBeenCalledWith({
      path: { id: 'provider-id' },
      body: { default_compatibilities: ['tool-call', 'reasoning'] },
      throwOnError: true,
    })
    app.unmount()
  })

  it('does not advance without a chat model', async () => {
    mocks.getProviderTemplates.mockResolvedValue({
      data: [{ id: 'template-id', domain: 'llm', key: 'deepseek' }],
    })
    mocks.postProviderFromTemplate.mockResolvedValue({ data: { id: 'provider-id' } })
    mocks.getProviderModels.mockResolvedValue({
      data: [{ id: 'embedding-id', model_id: 'embedding', type: 'embedding', enable: false }],
    })
    const { app, ready, setup } = mountSetup(preset)
    setup.formValues.value.api_key = 'sk-test'

    await setup.saveAndNext()

    expect(ready).not.toHaveBeenCalled()
    expect(setup.errorState.value).toBe('noModels')
    app.unmount()
  })
})
