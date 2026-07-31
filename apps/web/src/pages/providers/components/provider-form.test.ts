// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import type { Slots } from 'vue'

const mocks = vi.hoisted(() => ({
  ensureProvider: vi.fn(),
  getAuthorize: vi.fn(),
  getOAuthStatus: vi.fn(),
  pollOAuth: vi.fn(),
  syncCatalog: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}))

function translate(key: string) {
  return key
}

async function flushPromises() {
  await Promise.resolve()
  await nextTick()
  await Promise.resolve()
  await nextTick()
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: translate }),
}))

vi.mock('@memohai/sdk', () => ({
  deleteProvidersByIdOauthToken: vi.fn(),
  getProvidersByIdOauthAuthorize: mocks.getAuthorize,
  getProvidersByIdOauthStatus: mocks.getOAuthStatus,
  postProvidersByIdOauthPoll: mocks.pollOAuth,
  postProvidersByIdTest: vi.fn(),
}))

vi.mock('@/composables/useProviderModelCatalog', () => ({
  useProviderModelCatalog: () => ({ syncProviderModelCatalog: mocks.syncCatalog }),
}))

vi.mock('lucide-vue-next', () => ({
  AlertCircle: () => h('span'),
  KeyRound: () => h('span'),
  RefreshCw: () => h('span'),
}))

vi.mock('@felinic/ui', async () => {
  const { h } = await import('vue')
  const Passthrough = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', slots.default?.())
  const FormField = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', slots.default?.({
    componentField: {},
    errorMessage: '',
  }))
  const Button = (_props: Record<string, unknown>, { attrs, slots }: { attrs: Record<string, unknown>, slots: Slots }) => h('button', attrs, slots.default?.())
  const DeviceCodePanel = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', { 'data-testid': 'device-code' }, slots.default?.())
  return {
    AutoHeight: Passthrough,
    Button,
    ConfirmPopover: Passthrough,
    DeviceCodePanel,
    SettingsRow: Passthrough,
    SettingsSection: Passthrough,
    FormControl: Passthrough,
    FormField,
    FormItem: Passthrough,
    FormMessage: Passthrough,
    HoverCard: Passthrough,
    HoverCardContent: Passthrough,
    HoverCardTrigger: Passthrough,
    Input: Passthrough,
    LabelSwap: Passthrough,
    Select: Passthrough,
    SelectContent: Passthrough,
    SelectItem: Passthrough,
    SelectTrigger: Passthrough,
    SelectValue: Passthrough,
    Spinner: Passthrough,
    toast: {
      success: mocks.toastSuccess,
      error: mocks.toastError,
    },
  }
})

vi.mock('@/components/loading-button/index.vue', () => ({ default: { template: '<div><slot /></div>' } }))

describe('provider OAuth model sync', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
    mocks.getOAuthStatus.mockResolvedValue({
      data: {
        configured: true,
        mode: 'device',
        has_token: false,
        expired: false,
        device: {
          pending: true,
          user_code: 'ABCD-EFGH',
          verification_uri: 'https://github.com/login/device',
          interval_seconds: 1,
        },
      },
    })
    mocks.pollOAuth.mockResolvedValue({
      data: {
        configured: true,
        mode: 'device',
        has_token: true,
        expired: false,
      },
    })
    mocks.getAuthorize.mockResolvedValue({
      data: {
        mode: 'device',
        device: {
          pending: true,
          user_code: 'ABCD-EFGH',
          verification_uri: 'https://github.com/login/device',
          interval_seconds: 1,
        },
      },
    })
    mocks.ensureProvider.mockResolvedValue({
      id: 'created-provider-id',
      name: 'GitHub Copilot',
      client_type: 'github-copilot',
      enable: false,
      config: {},
    })
    mocks.syncCatalog.mockResolvedValue({ created: 2, updated: 1 })
  })

  afterEach(() => {
    vi.useRealTimers()
    document.body.innerHTML = ''
  })

  it('syncs the managed catalog when device authorization completes', async () => {
    const providerForm = (await import('./provider-form.vue')).default
    const root = document.createElement('div')
    document.body.append(root)
    // eslint-disable-next-line vue/one-component-per-file -- The object is root props, not a component definition.
    const app = createApp(providerForm, {
      provider: {
        id: 'provider-id',
        name: 'GitHub Copilot',
        client_type: 'github-copilot',
        enable: true,
        config: {},
      },
      editLoading: false,
      ensureProvider: mocks.ensureProvider,
    })
    app.config.globalProperties.$t = translate
    app.mount(root)
    await flushPromises()

    expect(mocks.getOAuthStatus).toHaveBeenCalledOnce()
    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()

    expect(mocks.pollOAuth).toHaveBeenCalledOnce()
    expect(mocks.syncCatalog).toHaveBeenCalledOnce()
    expect(mocks.syncCatalog).toHaveBeenCalledWith('provider-id')
    expect(mocks.toastSuccess).toHaveBeenCalledWith('provider.oauth.authorizeSuccess')

    app.unmount()
    root.remove()
  })

  it('materializes a template before starting OAuth authorization', async () => {
    const providerForm = (await import('./provider-form.vue')).default
    const root = document.createElement('div')
    document.body.append(root)
    // eslint-disable-next-line vue/one-component-per-file -- The object is root props, not a component definition.
    const app = createApp(providerForm, {
      provider: {
        provider_template_id: 'template-copilot',
        name: 'GitHub Copilot',
        client_type: 'github-copilot',
        enable: false,
        config: {},
      },
      editLoading: false,
      ensureProvider: mocks.ensureProvider,
    })
    app.config.globalProperties.$t = translate
    app.mount(root)
    await flushPromises()

    ;(root.querySelector('button') as HTMLButtonElement).click()
    await flushPromises()

    expect(mocks.ensureProvider).toHaveBeenCalledOnce()
    expect(mocks.getAuthorize).toHaveBeenCalledWith({
      path: { id: 'created-provider-id' },
      throwOnError: true,
    })
    expect(mocks.ensureProvider.mock.invocationCallOrder[0])
      .toBeLessThan(mocks.getAuthorize.mock.invocationCallOrder[0]!)

    app.unmount()
    root.remove()
  })

  it('keeps the new device code when the pre-authorization status request finishes later', async () => {
    let resolveStatus!: (value: { data: Record<string, unknown> }) => void
    mocks.getOAuthStatus.mockImplementationOnce(() => new Promise((resolve) => {
      resolveStatus = resolve
    }))

    const providerForm = (await import('./provider-form.vue')).default
    const provider = ref<Record<string, unknown>>({
      provider_template_id: 'template-copilot',
      name: 'GitHub Copilot',
      client_type: 'github-copilot',
      enable: false,
      config: {},
    })
    const ensureProvider = vi.fn(async () => {
      const created = {
        id: 'created-provider-id',
        name: 'GitHub Copilot',
        client_type: 'github-copilot',
        enable: false,
        config: {},
      }
      provider.value = created
      await nextTick()
      return created
    })
    // eslint-disable-next-line vue/one-component-per-file -- Test wrapper keeps the provider prop reactive during materialization.
    const Wrapper = defineComponent({
      setup() {
        return () => h(providerForm, {
          provider: provider.value,
          editLoading: false,
          ensureProvider,
        })
      },
    })
    const root = document.createElement('div')
    document.body.append(root)
    const app = createApp(Wrapper)
    app.config.globalProperties.$t = translate
    app.mount(root)
    await flushPromises()

    ;(root.querySelector('button') as HTMLButtonElement).click()
    await flushPromises()

    expect(root.querySelector('[data-testid="device-code"]')).not.toBeNull()

    resolveStatus({
      data: {
        configured: true,
        mode: 'device',
        has_token: false,
        expired: false,
      },
    })
    await flushPromises()

    expect(root.querySelector('[data-testid="device-code"]')).not.toBeNull()

    app.unmount()
    root.remove()
  })
})
