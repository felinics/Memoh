// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import type { Slots } from 'vue'

const mocks = vi.hoisted(() => ({
  postProvidersFromTemplate: vi.fn(),
  postProvidersByIdImportModels: vi.fn(),
  putProvidersById: vi.fn(),
  deleteModelsById: vi.fn(),
  deleteProvidersById: vi.fn(),
  getProvidersByIdModels: vi.fn(),
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
    useMutation: (options: { mutation: (...args: unknown[]) => Promise<unknown> }) => ({
      mutate: options.mutation,
      mutateAsync: options.mutation,
      isLoading: ref(false),
    }),
    useQueryCache: () => ({ invalidateQueries: vi.fn() }),
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
vi.mock('./components/provider-form.vue', () => ({
  // eslint-disable-next-line vue/one-component-per-file -- Test double for the child form.
  default: defineComponent({
    props: { provider: { type: Object, default: undefined } },
    setup: () => () => h('div', { 'data-testid': 'provider-form' }),
  }),
}))
vi.mock('./components/model-list.vue', () => ({
  // eslint-disable-next-line vue/one-component-per-file -- Test double for the child list.
  default: defineComponent({
    props: { providerId: { type: String, default: undefined } },
    setup: () => () => h('div', { 'data-testid': 'model-list' }),
  }),
}))
vi.mock('lucide-vue-next', () => ({
  Trash2: () => h('span'),
}))

vi.mock('@felinic/ui', async () => {
  const { defineComponent: define, h: createElement } = await import('vue')
  const Passthrough = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', slots.default?.())
  return {
    Button: Passthrough,
    ConfirmPopover: Passthrough,
    SettingsShell: Passthrough,
    Switch: define({
      props: ['modelValue', 'disabled'],
      emits: ['update:modelValue'],
      setup(props: { modelValue?: boolean }, { emit }: { emit: (event: 'update:modelValue', value: boolean) => void }) {
        return () => createElement('button', {
          'data-testid': 'provider-enable',
          'onClick': () => emit('update:modelValue', !props.modelValue),
        })
      },
    }),
    toast: {
      error: vi.fn(),
      success: vi.fn(),
    },
  }
})

describe('provider draft enable', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.postProvidersFromTemplate.mockResolvedValue({
      data: {
        id: 'created-provider',
        name: 'New API',
        client_type: 'openai-completions',
        enable: true,
        config: {},
      },
    })
    mocks.postProvidersByIdImportModels.mockResolvedValue({ data: { created: 1, skipped: 0 } })
    mocks.putProvidersById.mockResolvedValue({ data: {} })
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('keeps a template draft local when enabling before save', async () => {
    const ModelSetting = (await import('./model-setting.vue')).default
    const provider = ref({
      provider_template_id: 'template-newapi',
      name: 'New API',
      client_type: 'openai-completions',
      enable: false,
      config: {
        base_url: 'https://aigw.example/v1',
        api_key: 'sk-test',
      },
    })
    // eslint-disable-next-line vue/one-component-per-file -- Test wrapper keeps the provider v-model reactive.
    const Wrapper = defineComponent({
      setup() {
        return () => h(ModelSetting, {
          provider: provider.value,
          'onUpdate:provider': (value: typeof provider.value) => {
            provider.value = value
          },
        })
      },
    })

    const root = document.createElement('div')
    document.body.append(root)
    const app = createApp(Wrapper)
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    await nextTick()

    ;(root.querySelector('[data-testid="provider-enable"]') as HTMLButtonElement).click()
    await flushPromises()

    expect(provider.value.enable).toBe(true)
    expect(mocks.postProvidersFromTemplate).not.toHaveBeenCalled()
    expect(mocks.postProvidersByIdImportModels).not.toHaveBeenCalled()
    expect(mocks.putProvidersById).not.toHaveBeenCalled()

    app.unmount()
    root.remove()
  })
})
