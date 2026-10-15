// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import type { Slots } from 'vue'
import type { ProvidersGetResponse } from '@memohai/sdk'

const mocks = vi.hoisted(() => ({
  postProviders: vi.fn(),
  postProvidersByIdImportModels: vi.fn(),
  putProvidersById: vi.fn(),
  deleteModelsById: vi.fn(),
  deleteProvidersById: vi.fn(),
  getProvidersByIdModels: vi.fn(),
  importProviderModels: vi.fn(),
  draftPayload: vi.fn(),
  canListModels: vi.fn(),
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
    defineQueryOptions: (options: unknown) => options,
  }
})

vi.mock('@memohai/sdk', () => ({
  deleteModelsById: mocks.deleteModelsById,
  deleteProvidersById: mocks.deleteProvidersById,
  getProvidersByIdModels: mocks.getProvidersByIdModels,
  postProvidersByIdImportModels: mocks.postProvidersByIdImportModels,
  postProviders: mocks.postProviders,
  putProvidersById: mocks.putProvidersById,
}))


vi.mock('@/composables/useProviderModelCatalog', () => ({
  useProviderModelCatalog: () => ({ importProviderModels: mocks.importProviderModels }),
}))

vi.mock('@/constants/client-types', () => ({
  isManagedModelCatalogClientType: () => false,
}))

vi.mock('@/components/provider-icon/index.vue', () => ({ default: () => h('span') }))
vi.mock('./components/provider-form.vue', () => ({
  // eslint-disable-next-line vue/one-component-per-file -- Test double for the child form.
  default: defineComponent({
    props: { provider: { type: Object, default: undefined } },
    setup: (_props, { expose }) => {
      expose({ draftPayload: mocks.draftPayload, canListModels: mocks.canListModels })
      return () => h('div', { 'data-testid': 'provider-form' })
    },
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
    SurfaceCard: (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', [slots.default?.(), slots.media?.(), slots.actions?.()]),
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

async function mountSetting(initial: ProvidersGetResponse) {
  const ModelSetting = (await import('./model-setting.vue')).default
  const provider = ref<ProvidersGetResponse>(initial)
  // eslint-disable-next-line vue/one-component-per-file -- Test wrapper keeps the provider v-model reactive.
  const Wrapper = defineComponent({
    setup() {
      return () => h(ModelSetting, {
        provider: provider.value,
        'onUpdate:provider': (value: ProvidersGetResponse | undefined) => {
          if (value) provider.value = value
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

  return {
    provider,
    async toggle() {
      ;(root.querySelector('[data-testid="provider-enable"]') as HTMLButtonElement).click()
      await flushPromises()
      await flushPromises()
    },
    unmount() {
      app.unmount()
      root.remove()
    },
  }
}

function sourceDraft(enable = false): ProvidersGetResponse {
  return {
    metadata: { catalog: 'aihubmix', source: 'aihubmix' },
    name: 'AIHubMix',
    client_type: 'openai-completions',
    enable,
    config: { base_url: 'https://aihubmix.com/v1' },
  }
}

function endpointDraft(): ProvidersGetResponse {
  return {
    metadata: { catalog: 'custom-endpoint' },
    name: 'Custom',
    client_type: 'openai-completions',
    enable: false,
    config: { base_url: 'https://custom.example/v1' },
  }
}

describe('provider draft enable', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.postProviders.mockResolvedValue({
      data: {
        id: 'created-provider',
        name: 'AIHubMix',
        client_type: 'openai-completions',
        enable: true,
        config: {},
      },
    })
    mocks.importProviderModels.mockResolvedValue({ created: 1, skipped: 0 })
    mocks.putProvidersById.mockResolvedValue({ data: { id: 'created-provider', enable: false } })
    mocks.draftPayload.mockReturnValue({
      name: 'AIHubMix',
      client_type: 'openai-completions',
      config: { base_url: 'https://form.example/v1', api_key: 'sk-unsaved' },
    })
    mocks.canListModels.mockReturnValue(false)
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('creates a source draft from the unsaved form values and imports its catalog', async () => {
    const view = await mountSetting(sourceDraft())

    await view.toggle()

    expect(mocks.postProviders).toHaveBeenCalledWith(expect.objectContaining({
      body: expect.objectContaining({
        name: 'AIHubMix',
        config: { base_url: 'https://form.example/v1', api_key: 'sk-unsaved' },
        metadata: { catalog: 'aihubmix', source: 'aihubmix' },
      }),
    }))
    expect(mocks.putProvidersById).not.toHaveBeenCalled()
    // A models.dev catalog needs no key, so it is imported regardless.
    expect(mocks.importProviderModels).toHaveBeenCalledWith(expect.objectContaining({ id: 'created-provider' }))
    expect(view.provider.value.id).toBe('created-provider')
    expect(view.provider.value.enable).toBe(true)

    view.unmount()
  })

  it('creates an endpoint draft without importing when the form cannot list models', async () => {
    const view = await mountSetting(endpointDraft())

    await view.toggle()

    expect(mocks.postProviders).toHaveBeenCalledTimes(1)
    expect(mocks.importProviderModels).not.toHaveBeenCalled()
    expect(view.provider.value.id).toBe('created-provider')

    view.unmount()
  })

  it('imports an endpoint draft once the form can list its models', async () => {
    mocks.canListModels.mockReturnValue(true)
    const view = await mountSetting(endpointDraft())

    await view.toggle()

    expect(mocks.importProviderModels).toHaveBeenCalledTimes(1)

    view.unmount()
  })

  it('creates a disabled provider when a draft is switched off', async () => {
    const view = await mountSetting(sourceDraft(true))

    await view.toggle()

    expect(mocks.postProviders).toHaveBeenCalledTimes(1)
    expect(mocks.putProvidersById).toHaveBeenCalledWith(expect.objectContaining({
      path: { id: 'created-provider' },
      body: { enable: false },
    }))
    expect(view.provider.value.enable).toBe(false)

    view.unmount()
  })

  it('rolls the switch back when creating the draft fails', async () => {
    mocks.postProviders.mockRejectedValue(new Error('boom'))
    const { toast } = await import('@felinic/ui')
    const view = await mountSetting(sourceDraft())

    await view.toggle()

    expect(view.provider.value.enable).toBe(false)
    expect(view.provider.value.id).toBeUndefined()
    expect(toast.error).toHaveBeenCalledWith('common.saveFailed')

    view.unmount()
  })

  it('updates an existing provider in place', async () => {
    const view = await mountSetting({ ...sourceDraft(), id: 'existing-provider' })

    await view.toggle()

    expect(mocks.postProviders).not.toHaveBeenCalled()
    expect(mocks.putProvidersById).toHaveBeenCalledWith(expect.objectContaining({
      path: { id: 'existing-provider' },
      body: { enable: true },
    }))

    view.unmount()
  })
})
