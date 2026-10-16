// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import type { Slots } from 'vue'

const mocks = vi.hoisted(() => ({
  deleteModelsById: vi.fn(),
  deleteProvidersById: vi.fn(),
  getProvidersByIdModels: vi.fn(),
  invalidateQueries: vi.fn(),
  postProvidersByIdImportModels: vi.fn(),
  postProvidersFromTemplate: vi.fn(),
  putProvidersById: vi.fn(),
}))

async function flushPromises() {
  await Promise.resolve()
  await nextTick()
  await Promise.resolve()
  await nextTick()
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('@pinia/colada', async () => {
  const { ref } = await import('vue')
  return {
    useQuery: () => ({ data: ref([]) }),
    useMutation: (options: {
      mutation: (...args: unknown[]) => Promise<unknown>
      onSettled?: () => void
    }) => ({
      mutate: async (...args: unknown[]) => {
        try {
          return await options.mutation(...args)
        } finally {
          options.onSettled?.()
        }
      },
      mutateAsync: async (...args: unknown[]) => {
        try {
          return await options.mutation(...args)
        } finally {
          options.onSettled?.()
        }
      },
      isLoading: ref(false),
    }),
    useQueryCache: () => ({ invalidateQueries: mocks.invalidateQueries }),
  }
})

vi.mock('@memohai/sdk', () => ({
  deleteModelsById: mocks.deleteModelsById,
  deleteProvidersById: mocks.deleteProvidersById,
  getProvidersByIdModels: mocks.getProvidersByIdModels,
  postProvidersByIdImportModels: mocks.postProvidersByIdImportModels,
  postProvidersFromTemplate: mocks.postProvidersFromTemplate,
  putProvidersById: mocks.putProvidersById,
}))

vi.mock('@/composables/useProviderTemplateModels', async () => {
  const { ref } = await import('vue')
  return { useProviderTemplateModels: () => ({ models: ref([]) }) }
})

vi.mock('@/constants/client-types', () => ({
  isManagedModelCatalogClientType: () => false,
}))

vi.mock('@/components/provider-icon/index.vue', () => ({ default: () => h('span') }))
vi.mock('./components/provider-form.vue', () => ({ default: () => h('div') }))
vi.mock('./components/model-list.vue', () => ({ default: () => h('div') }))
vi.mock('lucide-vue-next', () => ({ Trash2: () => h('span') }))

vi.mock('@felinic/ui', async () => {
  const { defineComponent: define, h: createElement } = await import('vue')
  const Passthrough = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => createElement('div', slots.default?.())
  return {
    Button: Passthrough,
    ConfirmPopover: define({
      emits: ['confirm'],
      setup(_props: Record<string, unknown>, { emit }: { emit: (event: 'confirm') => void }) {
        return () => createElement('button', {
          'data-testid': 'delete-confirm',
          'onClick': () => emit('confirm'),
        })
      },
    }),
    SettingsShell: Passthrough,
    Switch: Passthrough,
    toast: {
      error: vi.fn(),
      success: vi.fn(),
    },
  }
})

describe('provider deletion cache invalidation', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.deleteProvidersById.mockResolvedValue({ data: undefined })
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('refreshes provider templates after deleting a provider', async () => {
    const ModelSetting = (await import('./model-setting.vue')).default
    const root = document.createElement('div')
    document.body.append(root)
    const app = createApp(ModelSetting, {
      provider: {
        id: 'provider-1',
        provider_template_id: 'template-1',
        name: 'Ollama',
        client_type: 'openai-completions',
        enable: true,
        config: {},
      },
    })
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    await nextTick()

    ;(root.querySelector('[data-testid="delete-confirm"]') as HTMLButtonElement).click()
    await flushPromises()

    expect(mocks.deleteProvidersById).toHaveBeenCalledWith({
      path: { id: 'provider-1' },
      throwOnError: true,
    })
    expect(mocks.invalidateQueries).toHaveBeenCalledWith({ key: ['provider-templates', 'llm'] })

    app.unmount()
    root.remove()
  })
})
