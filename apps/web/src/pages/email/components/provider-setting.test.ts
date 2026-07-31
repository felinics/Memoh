// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, provide, ref } from 'vue'
import type { Component, Slots } from 'vue'
import type { EmailProviderResponse } from '@memohai/sdk'

const mocks = vi.hoisted(() => ({
  authorize: vi.fn(),
  createProvider: vi.fn(),
  getOAuthStatus: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
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
  useI18n: () => ({ t: translate, te: () => false }),
}))

vi.mock('vee-validate', async () => {
  const { reactive } = await import('vue')
  return {
    useForm: () => {
      const values = reactive({ name: '' })
      return {
        handleSubmit: (submit: (values: { name: string }) => unknown) => async (event?: Event) => {
          event?.preventDefault()
          return submit({ ...values })
        },
        setValues: (next: { name: string }) => Object.assign(values, next),
      }
    },
  }
})

vi.mock('@pinia/colada', async () => {
  const { ref } = await import('vue')
  return {
    useMutation: ({ mutation }: { mutation: (data?: unknown) => Promise<unknown> }) => ({
      mutateAsync: (data?: unknown) => mutation(data),
      isLoading: ref(false),
    }),
    useQuery: () => ({
      data: ref([{
        provider: 'gmail',
        display_name: 'Gmail',
        config_schema: { fields: [] },
      }]),
    }),
    useQueryCache: () => ({ invalidateQueries: vi.fn() }),
  }
})

vi.mock('@memohai/sdk', () => ({
  deleteEmailProvidersById: vi.fn(),
  deleteEmailProvidersByIdOauthToken: vi.fn(),
  getEmailProvidersByIdOauthAuthorize: mocks.authorize,
  getEmailProvidersByIdOauthStatus: mocks.getOAuthStatus,
  getEmailProvidersMeta: vi.fn(),
  postEmailProviders: mocks.createProvider,
  putEmailProvidersById: vi.fn(),
}))

vi.mock('lucide-vue-next', () => ({
  Eye: () => h('span'),
  EyeOff: () => h('span'),
  KeyRound: () => h('span'),
  Trash2: () => h('span'),
}))

vi.mock('@felinic/ui', async () => {
  const { h } = await import('vue')
  const Passthrough = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', slots.default?.())
  const Button = (_props: Record<string, unknown>, { attrs, slots }: { attrs: Record<string, unknown>, slots: Slots }) => h('button', attrs, slots.default?.())
  const FormField = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', slots.default?.({
    componentField: {},
  }))
  const Section = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('section', [slots.default?.(), slots.footer?.()])
  return {
    Button,
    ConfirmPopover: Passthrough,
    SettingsShell: Passthrough,
    SettingsSection: Section,
    SettingsRow: Passthrough,
    FieldStack: Passthrough,
    FormControl: Passthrough,
    FormField,
    Input: Passthrough,
    Select: Passthrough,
    SelectContent: Passthrough,
    SelectItem: Passthrough,
    SelectTrigger: Passthrough,
    SelectValue: Passthrough,
    Switch: Passthrough,
    toast: {
      error: mocks.toastError,
      success: mocks.toastSuccess,
    },
  }
})

vi.mock('@/components/loading-button/index.vue', () => ({
  default: (_props: Record<string, unknown>, { attrs, slots }: { attrs: Record<string, unknown>, slots: Slots }) => h('button', attrs, slots.default?.()),
}))
vi.mock('@/components/email-provider-icon/index.vue', () => ({ default: () => h('span') }))

let ProviderSetting: Component

describe('Gmail provider OAuth', () => {
  beforeEach(async () => {
    vi.clearAllMocks()
    ProviderSetting = (await import('./provider-setting.vue')).default
    mocks.createProvider.mockResolvedValue({
      data: {
        id: 'gmail-created',
        name: 'Gmail',
        provider: 'gmail',
        config: {},
      },
    })
    mocks.authorize.mockResolvedValue({
      data: { auth_url: 'https://accounts.google.com/oauth' },
    })
    mocks.getOAuthStatus.mockResolvedValue({
      data: {
        configured: true,
        has_token: true,
        expired: false,
        email_address: 'person@gmail.com',
      },
    })
  })

  afterEach(() => {
    vi.restoreAllMocks()
    document.body.innerHTML = ''
  })

  it('creates the template draft before requesting the authorization URL', async () => {
    const provider = ref<EmailProviderResponse>({
      name: 'Gmail',
      provider: 'gmail',
      config: {},
    })
    const popup = {
      close: vi.fn(),
      focus: vi.fn(),
      location: { href: '' },
    }
    vi.spyOn(window, 'open').mockReturnValue(popup as unknown as Window)

    const Wrapper = defineComponent({
      setup() {
        provide('curEmailProvider', provider)
        return () => h(ProviderSetting)
      },
    })
    const root = document.createElement('div')
    document.body.append(root)
    const app = createApp(Wrapper)
    app.config.globalProperties.$t = translate
    app.mount(root)
    await flushPromises()

    const authorizeButton = Array.from(root.querySelectorAll('button'))
      .find(button => button.textContent?.includes('email.oauth.authorize'))
    expect(authorizeButton).toBeDefined()
    authorizeButton!.click()
    await flushPromises()

    expect(window.open).toHaveBeenCalledWith('', 'email-oauth', 'width=600,height=720')
    expect(mocks.createProvider).toHaveBeenCalledWith({
      body: {
        name: 'Gmail',
        provider: 'gmail',
        config: {},
      },
      throwOnError: true,
    })
    expect(mocks.authorize).toHaveBeenCalledWith({
      path: { id: 'gmail-created' },
    })
    expect(mocks.createProvider.mock.invocationCallOrder[0])
      .toBeLessThan(mocks.authorize.mock.invocationCallOrder[0]!)
    expect(popup.location.href).toBe('https://accounts.google.com/oauth')
    expect(popup.focus).toHaveBeenCalledOnce()

    window.dispatchEvent(new MessageEvent('message', {
      data: {
        type: 'memoh-email-oauth-callback',
        providerId: 'gmail-created',
        status: 'success',
      },
    }))
    await flushPromises()

    expect(provider.value.id).toBe('gmail-created')
    expect(mocks.toastSuccess).toHaveBeenCalledWith('email.oauth.authorizeOpened')

    app.unmount()
    root.remove()
  })
})
