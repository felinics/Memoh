// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, inject, nextTick, ref } from 'vue'
import type { Component, Ref, Slots } from 'vue'

const mocks = vi.hoisted(() => ({
  providerData: undefined as unknown as Ref<Array<Record<string, unknown>> | undefined>,
  providersLoading: undefined as unknown as Ref<boolean>,
  providersError: undefined as unknown as Ref<Error | null>,
  refetchSearchProviders: vi.fn(),
  fetchProviderData: undefined as unknown as Ref<Array<Record<string, unknown>> | undefined>,
  fetchProvidersLoading: undefined as unknown as Ref<boolean>,
  fetchProvidersError: undefined as unknown as Ref<Error | null>,
  refetchFetchProviders: vi.fn(),
  searchProviderMetaData: undefined as unknown as Ref<Array<Record<string, unknown>> | undefined>,
  fetchProviderMetaData: undefined as unknown as Ref<Array<Record<string, unknown>> | undefined>,
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
  mocks.providersError = ref(null)
  mocks.fetchProviderData = ref()
  mocks.fetchProvidersLoading = ref(false)
  mocks.fetchProvidersError = ref(null)
  mocks.searchProviderMetaData = ref([])
  mocks.fetchProviderMetaData = ref([])
  return {
    useQuery: ({ key }: { key: () => string[] }) => {
      switch (key()[0]) {
        case 'search-providers':
          return {
            data: mocks.providerData,
            isLoading: mocks.providersLoading,
            error: mocks.providersError,
            refetch: mocks.refetchSearchProviders,
          }
        case 'fetch-providers':
          return {
            data: mocks.fetchProviderData,
            isLoading: mocks.fetchProvidersLoading,
            error: mocks.fetchProvidersError,
            refetch: mocks.refetchFetchProviders,
          }
        case 'search-providers-meta':
          return { data: mocks.searchProviderMetaData, isLoading: ref(false), error: ref(null), refetch: vi.fn() }
        case 'fetch-providers-meta':
          return { data: mocks.fetchProviderMetaData, isLoading: ref(false), error: ref(null), refetch: vi.fn() }
        default:
          return { data: ref(), isLoading: ref(false), error: ref(null), refetch: vi.fn() }
      }
    },
  }
})

vi.mock('@memohai/sdk', () => ({
  getSearchProviders: vi.fn(),
  getFetchProviders: vi.fn(),
  getSearchProvidersMeta: vi.fn(),
  getFetchProvidersMeta: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, fallback?: string) => fallback ?? key,
  }),
}))

vi.mock('@/utils/api-error', () => ({
  resolveApiErrorMessage: (err: unknown, fallback: string) => {
    if (err instanceof Error && err.message.startsWith('API:')) return err.message
    return fallback
  },
}))

vi.mock('@felinic/ui', () => {
  const Passthrough = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', slots.default?.())
  const BackendCard = {
    props: ['name', 'enabled'],
    emits: ['click'],
    setup(props: { name: string, enabled?: boolean }, { emit }: { emit: (event: 'click') => void }) {
      return () => h('button', {
        'data-provider': props.name,
        'data-enabled': String(Boolean(props.enabled)),
        'onClick': () => emit('click'),
      }, props.name)
    },
  }
  const Button = {
    props: ['loading', 'variant', 'size', 'type'],
    emits: ['click'],
    setup(props: { loading?: boolean }, { slots, emit }: { slots: Slots, emit: (event: 'click') => void }) {
      return () => h('button', {
        'type': 'button',
        'data-slot': 'button',
        'data-loading': String(Boolean(props.loading)),
        'onClick': () => emit('click'),
      }, slots.default?.())
    },
  }
  const CalloutBanner = {
    props: ['tone', 'title', 'description'],
    setup(props: { tone?: string, title?: string, description?: string }, { slots }: { slots: Slots }) {
      return () => h('div', {
        'data-slot': 'callout-banner',
        'data-tone': props.tone ?? 'warning',
        'role': props.tone === 'destructive' ? 'alert' : 'status',
      }, [
        props.title ? h('p', { 'data-testid': 'banner-title' }, props.title) : null,
        props.description ? h('p', { 'data-testid': 'banner-description' }, props.description) : null,
        slots.default?.(),
      ])
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
  const SettingsShell = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', { 'data-testid': 'settings-shell' }, slots.default?.())
  const SwapTransition = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', slots.default?.())
  const PageShell = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', [slots.actions?.(), slots.default?.()])
  const SectionGroup = {
    props: ['title'],
    setup(props: { title: string }, { slots }: { slots: Slots }) {
      return () => h('section', { 'data-section': props.title }, slots.default?.())
    },
  }
  return {
    BackendCard,
    Button,
    CalloutBanner,
    DetailPane,
    PageShell,
    SectionGroup,
    SettingsShell,
    SwapTransition,
    Skeleton: Passthrough,
  }
})

vi.mock('@/components/search-provider-logo/index.vue', () => ({ default: () => h('span') }))
vi.mock('@/components/backend-card-grid-skeleton/index.vue', () => ({
  default: (props: { count?: number }) => h('div', {
    'data-slot': 'skeleton',
    'data-count': String(props.count ?? 4),
  }),
}))

vi.mock('./components/provider-setting.vue', () => ({
  default: {
    emits: ['materialized'],
    setup() {
      const cur = inject<Ref<Record<string, unknown> | undefined>>('curSearchProvider')
      return () => h('div', {
        'data-testid': 'search-provider-detail',
        'data-id': String(cur?.value?.id ?? ''),
        'data-enable': String(Boolean(cur?.value?.enable)),
        'data-api-key': String((cur?.value?.config as Record<string, unknown> | undefined)?.api_key ?? ''),
      }, `${cur?.value?.id ?? 'draft'}:${cur?.value?.provider ?? ''}`)
    },
  },
}))

vi.mock('./components/fetch-provider-setting.vue', () => ({
  default: {
    emits: ['materialized'],
    setup() {
      const cur = inject<Ref<Record<string, unknown> | undefined>>('curFetchProvider')
      return () => h('div', {
        'data-testid': 'fetch-provider-detail',
        'data-id': String(cur?.value?.id ?? ''),
        'data-enable': String(Boolean(cur?.value?.enable)),
        'data-api-key': String((cur?.value?.config as Record<string, unknown> | undefined)?.api_key ?? ''),
      }, `${cur?.value?.id ?? 'draft'}:${cur?.value?.provider ?? ''}`)
    },
  },
}))

let WebSearchPage: Component

async function mountPage(searchQuery = '') {
  const root = document.createElement('div')
  document.body.append(root)
  const query = ref(searchQuery)
  const app = createApp({ render: () => h(WebSearchPage, { searchQuery: query.value }) })
  app.mount(root)
  await nextTick()
  return { app, root, query }
}

function sectionOf(root: Element, title: string) {
  return root.querySelector(`[data-section="${title}"]`)
}

function cardNames(scope: Element | null) {
  if (!scope) return []
  return [...scope.querySelectorAll('[data-provider]')].map(card => card.getAttribute('data-provider'))
}

describe('web-search page loading, readiness, and failure states', () => {
  beforeEach(async () => {
    vi.resetModules()
    WebSearchPage = (await import('./index.vue')).default
    mocks.providerData.value = [
      { id: 's-brave', name: 'Brave Search', provider: 'brave', enable: true, config: { api_key: 'brave-secret' } },
    ]
    mocks.providersLoading.value = false
    mocks.providersError.value = null
    mocks.refetchSearchProviders.mockReset()
    mocks.fetchProviderData.value = [
      { id: 'f-jina', name: 'Jina Reader', provider: 'jina', enable: true, config: { api_key: 'jina-secret' } },
    ]
    mocks.fetchProvidersLoading.value = false
    mocks.fetchProvidersError.value = null
    mocks.refetchFetchProviders.mockReset()
    mocks.searchProviderMetaData.value = []
    mocks.fetchProviderMetaData.value = []
    mocks.route.query = {}
    mocks.replace.mockReset()
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('shows card skeletons instead of fake disabled cards while initial list queries load, and keeps cached cards on refresh', async () => {
    mocks.providerData.value = undefined
    mocks.providersLoading.value = true
    mocks.fetchProviderData.value = undefined
    mocks.fetchProvidersLoading.value = true

    const { app, root } = await mountPage()
    const searchSection = sectionOf(root, 'webSearch.searchProviders')
    const fetchSection = sectionOf(root, 'webSearch.fetchProviders')

    expect(searchSection?.querySelector('[data-slot="skeleton"]')).not.toBeNull()
    expect(fetchSection?.querySelector('[data-slot="skeleton"]')).not.toBeNull()
    expect(root.querySelector('[data-provider]')).toBeNull()

    mocks.providerData.value = [
      { id: 's-tavily', name: 'Tavily Live', provider: 'tavily', enable: true, config: { api_key: 'tvly-1' } },
    ]
    mocks.providersLoading.value = false
    mocks.fetchProviderData.value = []
    mocks.fetchProvidersLoading.value = false
    await nextTick()

    expect(root.querySelector('[data-slot="skeleton"]')).toBeNull()
    expect(cardNames(searchSection)).toContain('Tavily Live')
    expect(searchSection?.querySelector('[data-provider="Tavily Live"]')?.getAttribute('data-enabled')).toBe('true')
    expect(cardNames(fetchSection)).toHaveLength(3)

    // Background refetch keeps existing cards mounted without flashing skeletons.
    mocks.providersLoading.value = true
    await nextTick()
    expect(searchSection?.querySelector('[data-slot="skeleton"]')).toBeNull()
    expect(cardNames(searchSection)).toContain('Tavily Live')
    app.unmount()
  })

  it('waits for search-providers before mounting a deep-linked search detail form', async () => {
    mocks.route.query = { webProvider: 'search:brave' }
    mocks.providerData.value = undefined
    mocks.providersLoading.value = true

    const { app, root } = await mountPage()

    expect(root.querySelector('[data-testid="detail-pane-skeleton"]')).not.toBeNull()
    expect(root.querySelector('[data-testid="search-provider-detail"]')).toBeNull()
    expect(mocks.replace).not.toHaveBeenCalled()

    mocks.providerData.value = [
      { id: 's-brave', name: 'Brave Search', provider: 'brave', enable: true, config: { api_key: 'brave-secret' } },
    ]
    mocks.providersLoading.value = false
    await nextTick()

    const detail = root.querySelector('[data-testid="search-provider-detail"]')
    expect(detail).not.toBeNull()
    expect(detail?.getAttribute('data-id')).toBe('s-brave')
    expect(detail?.getAttribute('data-enable')).toBe('true')
    expect(detail?.getAttribute('data-api-key')).toBe('brave-secret')
    expect(root.querySelector('[data-testid="detail-pane-skeleton"]')).toBeNull()
    app.unmount()
  })

  it('waits for fetch-providers before mounting a deep-linked fetch detail form', async () => {
    mocks.route.query = { webProvider: 'fetch:jina' }
    mocks.fetchProviderData.value = undefined
    mocks.fetchProvidersLoading.value = true

    const { app, root } = await mountPage()

    expect(root.querySelector('[data-testid="detail-pane-skeleton"]')).not.toBeNull()
    expect(root.querySelector('[data-testid="fetch-provider-detail"]')).toBeNull()
    expect(mocks.replace).not.toHaveBeenCalled()

    mocks.fetchProviderData.value = [
      { id: 'f-jina', name: 'Jina Reader', provider: 'jina', enable: true, config: { api_key: 'jina-secret' } },
    ]
    mocks.fetchProvidersLoading.value = false
    await nextTick()

    const detail = root.querySelector('[data-testid="fetch-provider-detail"]')
    expect(detail).not.toBeNull()
    expect(detail?.getAttribute('data-id')).toBe('f-jina')
    expect(detail?.getAttribute('data-api-key')).toBe('jina-secret')
    app.unmount()
  })

  it('redirects an unknown deep-linked provider back to list only after its list query resolves', async () => {
    mocks.route.query = { webProvider: 'search:unknown', scope: 'web' }
    mocks.providerData.value = undefined
    mocks.providersLoading.value = true

    const { app } = await mountPage()
    expect(mocks.replace).not.toHaveBeenCalled()

    mocks.providerData.value = []
    mocks.providersLoading.value = false
    await nextTick()

    expect(mocks.replace).toHaveBeenCalledWith({ query: { scope: 'web' } })
    app.unmount()
  })

  it('renders an error CalloutBanner with retry instead of fake cards when a list query fails without cached data', async () => {
    mocks.providerData.value = undefined
    mocks.providersLoading.value = false
    mocks.providersError.value = new Error('network down')

    const { app, root } = await mountPage()
    const searchSection = sectionOf(root, 'webSearch.searchProviders')
    const fetchSection = sectionOf(root, 'webSearch.fetchProviders')

    // Search section shows the destructive CalloutBanner and zero fake provider cards;
    // Fetch section still renders its healthy cards independently.
    expect(cardNames(searchSection)).toEqual([])
    const banner = searchSection?.querySelector('[data-slot="callout-banner"]')
    expect(banner).not.toBeNull()
    expect(banner?.getAttribute('data-tone')).toBe('destructive')
    expect(cardNames(fetchSection)).toHaveLength(3)

    const retryBtn = banner?.querySelector('button') as HTMLButtonElement | null
    expect(retryBtn).not.toBeNull()
    mocks.refetchSearchProviders.mockImplementation(async () => {
      mocks.providersError.value = null
      mocks.providerData.value = [
        { id: 's-brave', name: 'Brave Search', provider: 'brave', enable: true, config: { api_key: 'brave-secret' } },
      ]
    })
    retryBtn!.click()
    await nextTick()
    await nextTick()

    expect(mocks.refetchSearchProviders).toHaveBeenCalledTimes(1)
    expect(searchSection?.querySelector('[data-slot="callout-banner"]')).toBeNull()
    expect(cardNames(searchSection)[0]).toBe('Brave Search')
    app.unmount()
  })

  it('renders an error CalloutBanner with retry on deep-linked search or fetch detail failure instead of an empty form or infinite skeleton', async () => {
    mocks.route.query = { webProvider: 'fetch:jina' }
    mocks.fetchProviderData.value = undefined
    mocks.fetchProvidersLoading.value = false
    mocks.fetchProvidersError.value = new Error('502 bad gateway')

    const { app, root } = await mountPage()

    expect(mocks.replace).not.toHaveBeenCalled()
    expect(root.querySelector('[data-testid="detail-pane-skeleton"]')).toBeNull()
    expect(root.querySelector('[data-testid="fetch-provider-detail"]')).toBeNull()
    const banner = root.querySelector('[data-testid="detail-pane"] [data-slot="callout-banner"]')
    expect(banner).not.toBeNull()
    expect(banner?.getAttribute('data-tone')).toBe('destructive')

    const retryBtn = banner?.querySelector('button') as HTMLButtonElement | null
    expect(retryBtn).not.toBeNull()
    mocks.refetchFetchProviders.mockImplementation(async () => {
      mocks.fetchProvidersError.value = null
      mocks.fetchProviderData.value = [
        { id: 'f-jina', name: 'Jina Reader', provider: 'jina', enable: true, config: { api_key: 'jina-secret' } },
      ]
    })
    retryBtn!.click()
    await nextTick()
    await nextTick()

    expect(mocks.refetchFetchProviders).toHaveBeenCalledTimes(1)
    expect(mocks.refetchSearchProviders).not.toHaveBeenCalled()
    expect(root.querySelector('[data-testid="detail-pane"] [data-slot="callout-banner"]')).toBeNull()
    const detail = root.querySelector('[data-testid="fetch-provider-detail"]')
    expect(detail).not.toBeNull()
    expect(detail?.getAttribute('data-id')).toBe('f-jina')
    expect(detail?.getAttribute('data-api-key')).toBe('jina-secret')
    app.unmount()
  })

  it('surfaces localized API error details on deep-linked search detail failure without duplicating generic fallback copy', async () => {
    mocks.route.query = { webProvider: 'search:brave' }
    mocks.providerData.value = undefined
    mocks.providersLoading.value = false
    mocks.providersError.value = new Error('API:Service unavailable')

    const { app, root } = await mountPage()

    expect(mocks.replace).not.toHaveBeenCalled()
    expect(root.querySelector('[data-testid="search-provider-detail"]')).toBeNull()
    const banner = root.querySelector('[data-testid="detail-pane"] [data-slot="callout-banner"]')
    expect(banner?.querySelector('[data-testid="banner-title"]')?.textContent).toBe('common.loadFailed')
    expect(banner?.querySelector('[data-testid="banner-description"]')?.textContent).toBe('API:Service unavailable')

    // Generic error without a distinct API message omits redundant description text.
    mocks.providersError.value = new Error('raw internal stack')
    await nextTick()
    expect(banner?.querySelector('[data-testid="banner-title"]')?.textContent).toBe('common.loadFailed')
    expect(banner?.querySelector('[data-testid="banner-description"]')).toBeNull()
    app.unmount()
  })
})
