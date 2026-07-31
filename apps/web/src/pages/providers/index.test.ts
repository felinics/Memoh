// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import type { Component, Ref, Slots } from 'vue'

const mocks = vi.hoisted(() => ({
  providerData: undefined as unknown as Ref<Array<Record<string, unknown>> | undefined>,
  providersLoading: undefined as unknown as Ref<boolean>,
  modelData: undefined as unknown as Ref<Array<Record<string, unknown>>>,
  templateData: undefined as unknown as Ref<Array<Record<string, unknown>> | undefined>,
  templatesLoading: undefined as unknown as Ref<boolean>,
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
  mocks.modelData = ref([])
  mocks.templateData = ref()
  mocks.templatesLoading = ref(false)
  return {
    useQuery: ({ key }: { key: () => string[] }) => {
      if (key()[0] === 'providers') return { data: mocks.providerData, isLoading: mocks.providersLoading }
      if (key()[0] === 'provider-templates') return { data: mocks.templateData, isLoading: mocks.templatesLoading }
      return { data: mocks.modelData, isLoading: ref(false) }
    },
  }
})

vi.mock('@memohai/sdk', () => ({
  getModels: vi.fn(),
  getProviders: vi.fn(),
  getProviderTemplates: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('lucide-vue-next', () => ({
  Boxes: () => h('span'),
  Box: () => h('span'),
  ChevronRight: () => h('span'),
  Plus: () => h('span'),
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
    props: ['backLabel', 'width', 'loading'],
    emits: ['back'],
    setup(
      props: { loading?: boolean },
      { slots, emit }: { slots: Slots, emit: (event: 'back') => void },
    ) {
      return () => h('div', { 'data-testid': 'detail-pane' }, [
        h('button', { 'data-testid': 'back', onClick: () => emit('back') }, 'back'),
        props.loading
          ? h('div', { 'data-testid': 'detail-pane-skeleton' })
          : slots.default?.(),
      ])
    },
  }
  const SwapTransition = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', slots.default?.())
  const PageShell = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', [slots.actions?.(), slots.default?.()])
  return {
    BackendCard,
    DetailPane,
    SwapTransition,
    PageShell,
    Button: Passthrough,
    Empty: Passthrough,
    EmptyContent: Passthrough,
    EmptyDescription: Passthrough,
    EmptyHeader: Passthrough,
    EmptyMedia: Passthrough,
    EmptyTitle: Passthrough,
    InputGroup: Passthrough,
    InputGroupAddon: Passthrough,
    InputGroupInput: Passthrough,
    Skeleton: (props: { class?: string }) => h('div', { 'data-slot': 'skeleton', class: props.class }),
  }
})

vi.mock('@/components/add-provider/index.vue', () => ({
  default: {
    props: ['open'],
    emits: ['update:open'],
    setup(props: { open?: boolean }) {
      return () => h('div', {
        'data-testid': 'add-provider-dialog',
        'data-open': String(props.open ?? false),
      })
    },
  },
}))
vi.mock('@/components/provider-icon/index.vue', () => ({ default: () => h('span') }))
vi.mock('./model-setting.vue', () => ({
  default: {
    props: ['provider'],
    emits: ['materialized'],
    setup(
      props: { provider?: { id?: string, name?: string, provider_template_id?: string } },
      { emit }: { emit: (event: 'materialized', provider: Record<string, unknown>) => void },
    ) {
      return () => h('div', { 'data-testid': 'provider-detail' }, [
        `${props.provider?.id}:${props.provider?.name}`,
        h('button', {
          'data-testid': 'materialize-provider',
          'onClick': () => emit('materialized', {
            id: 'provider-anthropic',
            name: props.provider?.name,
            provider_template_id: props.provider?.provider_template_id,
          }),
        }),
      ])
    },
  },
}))

let ProviderPage: Component

async function mountPage() {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(ProviderPage)
  app.mount(root)
  await nextTick()
  return { app, root }
}

describe('provider route state', () => {
  beforeEach(async () => {
    vi.resetModules()
    ProviderPage = (await import('./index.vue')).default
    mocks.providerData.value = [
      { id: 'provider-one', name: 'One', enable: true },
      { id: 'provider-two', name: 'Two', enable: true },
    ]
    mocks.providersLoading.value = false
    mocks.modelData.value = []
    mocks.templateData.value = []
    mocks.templatesLoading.value = false
    mocks.route.query = {}
    mocks.replace.mockReset()
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('shows a detail skeleton while the URL provider is still loading', async () => {
    mocks.route.query = { provider: 'provider-two' }
    mocks.providerData.value = undefined
    mocks.providersLoading.value = true

    const { app, root } = await mountPage()

    expect(root.querySelector('[data-testid="detail-pane-skeleton"]')).not.toBeNull()
    expect(root.querySelector('[data-testid="provider-detail"]')).toBeNull()
    expect(mocks.replace).not.toHaveBeenCalled()
    app.unmount()
  })

  it('waits for a stale provider query to refresh before resolving the URL', async () => {
    mocks.route.query = { provider: 'provider-two' }
    mocks.providerData.value = [
      { id: 'provider-one', name: 'One', enable: true },
    ]
    mocks.providersLoading.value = true

    const { app, root } = await mountPage()

    expect(mocks.replace).not.toHaveBeenCalled()
    expect(root.querySelector('[data-testid="detail-pane-skeleton"]')).not.toBeNull()
    expect(root.querySelector('[data-testid="provider-detail"]')).toBeNull()

    mocks.providerData.value = [
      { id: 'provider-one', name: 'One', enable: true },
      { id: 'provider-two', name: 'Two', enable: true },
    ]
    mocks.providersLoading.value = false
    await nextTick()

    expect(root.querySelector('[data-testid="provider-detail"]')?.textContent).toBe('provider-two:Two')
    expect(root.querySelector('[data-testid="detail-pane-skeleton"]')).toBeNull()
    app.unmount()
  })

  it('writes the selected provider ID instead of a shared detail flag', async () => {
    const { app, root } = await mountPage()

    ;(root.querySelector('[data-provider="Two"]') as HTMLButtonElement).click()
    await nextTick()

    expect(mocks.replace).toHaveBeenCalledWith({ query: { provider: 'provider-two' } })
    expect(root.querySelector('[data-testid="provider-detail"]')?.textContent).toBe('provider-two:Two')
    app.unmount()
  })

  it('opens an unconfigured template as a draft detail without opening the add dialog', async () => {
    mocks.templateData.value = [{
      id: 'template-anthropic',
      name: 'Anthropic',
      driver: 'anthropic-messages',
      default_config: { base_url: 'https://api.anthropic.com' },
      configured: false,
    }]
    const { app, root } = await mountPage()

    ;(root.querySelector('[data-provider="Anthropic"]') as HTMLButtonElement).click()
    await nextTick()

    expect(mocks.replace).toHaveBeenCalledWith({ query: { provider: 'template:template-anthropic' } })
    expect(root.querySelector('[data-testid="provider-detail"]')?.textContent).toBe('undefined:Anthropic')
    expect(root.querySelector('[data-testid="add-provider-dialog"]')).toBeNull()
    app.unmount()
  })

  it('keeps the template route and detail open after the draft is materialized', async () => {
    mocks.templateData.value = [{
      id: 'template-anthropic',
      name: 'Anthropic',
      driver: 'anthropic-messages',
      default_config: { base_url: 'https://api.anthropic.com' },
      configured: false,
    }]
    const { app, root } = await mountPage()

    ;(root.querySelector('[data-provider="Anthropic"]') as HTMLButtonElement).click()
    await nextTick()
    ;(root.querySelector('[data-testid="materialize-provider"]') as HTMLButtonElement).click()
    await nextTick()

    expect(mocks.replace).toHaveBeenLastCalledWith({ query: { provider: 'template:template-anthropic' } })
    expect(root.querySelector('[data-testid="provider-detail"]')?.textContent).toContain('provider-anthropic:Anthropic')
    app.unmount()
  })

  it('returns to the list when the URL references a missing provider', async () => {
    mocks.route.query = { provider: 'missing', tab: 'kept' }

    const { app, root } = await mountPage()

    expect(mocks.replace).toHaveBeenCalledWith({ query: { tab: 'kept' } })
    expect(root.querySelector('[data-testid="provider-detail"]')).toBeNull()
    app.unmount()
  })
})
