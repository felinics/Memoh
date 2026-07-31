// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, inject, nextTick } from 'vue'
import type { Component, Ref, Slots } from 'vue'

const mocks = vi.hoisted(() => ({
  providerData: undefined as unknown as Ref<Array<Record<string, unknown>> | undefined>,
  providersLoading: undefined as unknown as Ref<boolean>,
  providerMetaData: undefined as unknown as Ref<Array<Record<string, unknown>> | undefined>,
  route: { query: {} as Record<string, string> },
  replace: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => mocks.route,
  useRouter: () => ({ replace: mocks.replace }),
}))

vi.mock('@pinia/colada', async () => {
  const { ref } = await import('vue')
  mocks.providerData = ref()
  mocks.providersLoading = ref(false)
  mocks.providerMetaData = ref()
  return {
    useQuery: ({ key }: { key: () => string[] }) => {
      if (key()[0] === 'email-providers') {
        return { data: mocks.providerData, isLoading: mocks.providersLoading }
      }
      return { data: mocks.providerMetaData, isLoading: ref(false) }
    },
  }
})

vi.mock('@memohai/sdk', () => ({
  getEmailProviders: vi.fn(),
  getEmailProvidersMeta: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('lucide-vue-next', () => ({
  Search: () => h('span'),
}))

vi.mock('@felinic/ui', () => {
  const Passthrough = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', slots.default?.())
  const BackendCard = {
    props: ['name'],
    emits: ['click'],
    setup(props: { name: string }, { emit }: { emit: (event: 'click') => void }) {
      return () => h('button', {
        'data-provider': props.name,
        'onClick': () => emit('click'),
      }, props.name)
    },
  }
  const DetailPane = {
    props: ['loading'],
    setup(props: { loading?: boolean }, { slots }: { slots: Slots }) {
      return () => h('div', { 'data-testid': 'detail-pane' },
        props.loading ? h('div', { 'data-testid': 'detail-loading' }) : slots.default?.(),
      )
    },
  }
  const SwapTransition = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', slots.default?.())
  const PageShell = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', [slots.actions?.(), slots.default?.()])
  return {
    BackendCard,
    DetailPane,
    SwapTransition,
    PageShell,
    InputGroup: Passthrough,
    InputGroupAddon: Passthrough,
    InputGroupInput: Passthrough,
  }
})

vi.mock('./components/add-email-provider.vue', () => ({
  default: {
    props: ['open'],
    emits: ['update:open'],
    setup(
      props: { open?: boolean },
      { emit }: { emit: (event: 'update:open', open: boolean) => void },
    ) {
      return () => h('button', {
        'data-testid': 'add-email-provider',
        'data-open': String(props.open ?? false),
        'onClick': () => emit('update:open', true),
      }, 'add')
    },
  },
}))

vi.mock('./components/provider-setting.vue', () => ({
  default: {
    emits: ['materialized'],
    setup(
      _props: Record<string, never>,
      { emit }: { emit: (event: 'materialized', provider: Record<string, unknown>) => void },
    ) {
      const provider = inject<Ref<Record<string, unknown> | undefined>>('curEmailProvider')!
      return () => h('div', { 'data-testid': 'provider-detail' }, [
        `${provider.value?.id ?? 'draft'}:${provider.value?.name ?? ''}`,
        h('button', {
          'data-testid': 'materialize-provider',
          'onClick': () => emit('materialized', {
            id: 'gmail-created',
            name: provider.value?.name,
            provider: provider.value?.provider,
            config: provider.value?.config,
          }),
        }),
      ])
    },
  },
}))

vi.mock('@/components/email-provider-icon/index.vue', () => ({ default: () => h('span') }))

let EmailPage: Component

async function mountPage() {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(EmailPage)
  app.mount(root)
  await nextTick()
  return { app, root }
}

describe('email provider catalog', () => {
  beforeEach(async () => {
    vi.resetModules()
    EmailPage = (await import('./index.vue')).default
    mocks.providerData.value = []
    mocks.providersLoading.value = false
    mocks.providerMetaData.value = [
      { provider: 'generic', display_name: 'Generic' },
      { provider: 'gmail', display_name: 'Gmail' },
      { provider: 'mailgun', display_name: 'Mailgun' },
    ]
    mocks.route.query = {}
    mocks.replace.mockReset()
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('keeps the add action and shows every real instance plus missing templates', async () => {
    mocks.providerData.value = [
      { id: 'gmail-personal', name: 'Personal Gmail', provider: 'gmail' },
      { id: 'gmail-work', name: 'Work Gmail', provider: 'gmail' },
    ]
    const { app, root } = await mountPage()

    expect(Array.from(root.querySelectorAll('[data-provider]')).map(item => item.textContent)).toEqual([
      'Generic',
      'Personal Gmail',
      'Work Gmail',
      'Mailgun',
    ])

    const add = root.querySelector('[data-testid="add-email-provider"]') as HTMLButtonElement
    expect(add.dataset.open).toBe('false')
    add.click()
    await nextTick()
    expect(add.dataset.open).toBe('true')
    app.unmount()
  })

  it('routes real instances by ID and missing templates by their draft identity', async () => {
    mocks.providerData.value = [
      { id: 'gmail-work', name: 'Work Gmail', provider: 'gmail' },
    ]
    const { app, root } = await mountPage()

    ;(root.querySelector('[data-provider="Work Gmail"]') as HTMLButtonElement).click()
    await nextTick()
    expect(mocks.replace).toHaveBeenLastCalledWith({ query: { emailProvider: 'gmail-work' } })

    mocks.route.query = {}
    app.unmount()
    document.body.innerHTML = ''

    const second = await mountPage()
    ;(second.root.querySelector('[data-provider="Mailgun"]') as HTMLButtonElement).click()
    await nextTick()
    expect(mocks.replace).toHaveBeenLastCalledWith({ query: { emailProvider: 'template:mailgun' } })
    second.app.unmount()
  })

  it('moves a saved template draft onto the created provider ID without closing detail', async () => {
    const { app, root } = await mountPage()

    ;(root.querySelector('[data-provider="Gmail"]') as HTMLButtonElement).click()
    await nextTick()
    expect(mocks.replace).toHaveBeenLastCalledWith({ query: { emailProvider: 'template:gmail' } })
    expect(root.querySelector('[data-testid="provider-detail"]')?.textContent).toContain('draft:Gmail')

    ;(root.querySelector('[data-testid="materialize-provider"]') as HTMLButtonElement).click()
    await nextTick()

    expect(mocks.replace).toHaveBeenLastCalledWith({ query: { emailProvider: 'gmail-created' } })
    expect(root.querySelector('[data-testid="provider-detail"]')?.textContent).toContain('gmail-created:Gmail')
    app.unmount()
  })
})
