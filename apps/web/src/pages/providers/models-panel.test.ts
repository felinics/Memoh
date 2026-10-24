// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref } from 'vue'
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
  const SectionGroup = {
    props: ['title'],
    setup(props: { title: string }, { slots }: { slots: Slots }) {
      return () => h('section', { 'data-section': props.title }, slots.default?.())
    },
  }
  return {
    BackendCard,
    SectionGroup,
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
    Pagination: {
      props: ['page', 'total', 'itemsPerPage'],
      emits: ['update:page'],
      setup(
        props: { page: number, total: number, itemsPerPage: number },
        { slots, emit }: { slots: Slots, emit: (event: 'update:page', page: number) => void },
      ) {
        return () => h('nav', {
          'data-testid': 'pagination',
          'data-page': String(props.page),
          'data-pages': String(Math.ceil(props.total / props.itemsPerPage)),
        }, [
          h('button', { 'data-testid': 'next-page', 'onClick': () => emit('update:page', props.page + 1) }, 'next'),
          slots.default?.(),
        ])
      },
    },
    PaginationContent: (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', slots.default?.({ items: [] })),
    PaginationEllipsis: Passthrough,
    PaginationFirst: Passthrough,
    PaginationItem: Passthrough,
    PaginationLast: Passthrough,
    PaginationNext: Passthrough,
    PaginationPrevious: Passthrough,
  }
})

vi.mock('@/components/add-provider/index.vue', () => ({
  default: {
    props: ['open', 'initialSource'],
    emits: ['update:open'],
    setup(props: { open?: boolean, initialSource?: string }) {
      return () => h('div', {
        'data-testid': 'add-provider-dialog',
        'data-open': String(props.open ?? false),
        'data-initial-source': props.initialSource ?? '',
      })
    },
  },
}))
vi.mock('@/components/provider-icon/index.vue', () => ({ default: () => h('span') }))
vi.mock('@/components/backend-card-grid-skeleton/index.vue', () => ({ default: () => h('div', { 'data-slot': 'skeleton' }) }))
vi.mock('./model-setting.vue', () => ({
  default: {
    props: ['provider'],
    emits: ['materialized'],
    setup(
      props: { provider?: { id?: string, name?: string, provider_template_id?: string, metadata?: Record<string, unknown> } },
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
            // The created provider records the draft's entry, which takes the
            // entry off the gallery before the route moves.
            metadata: props.provider?.metadata,
          }),
        }),
      ])
    },
  },
}))

let ModelsPanel: Component

async function mountPage(searchQuery = '') {
  const root = document.createElement('div')
  document.body.append(root)
  // The container page owns the search box; the panel only receives its query.
  const query = ref(searchQuery)
  const app = createApp({ render: () => h(ModelsPanel, { searchQuery: query.value }) })
  app.mount(root)
  await nextTick()
  return { app, root, query }
}

function cardNames(root: Element) {
  return [...root.querySelectorAll('[data-provider]')].map(card => card.getAttribute('data-provider'))
}

function sectionTitles(root: Element) {
  return [...root.querySelectorAll('[data-section]')].map(section => section.getAttribute('data-section'))
}

describe('models panel route state', () => {
  beforeEach(async () => {
    vi.resetModules()
    ModelsPanel = (await import('./models-panel.vue')).default
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

  it.each(['providers'] as const)('keeps cached cards mounted while %s refresh', async (source) => {
    const { app, root } = await mountPage()
    const cards = [...root.querySelectorAll('[data-provider]')]
    expect(cardNames(root).slice(0, 2)).toEqual(['One', 'Two'])

    const loading = source === 'providers' ? mocks.providersLoading : mocks.templatesLoading
    loading.value = true
    await nextTick()

    expect([...root.querySelectorAll('[data-provider]')]).toEqual(cards)
    expect(root.querySelector('[data-slot="skeleton"]')).toBeNull()
    loading.value = false
    await nextTick()
    expect([...root.querySelectorAll('[data-provider]')]).toEqual(cards)
    app.unmount()
  })

  it.each(['providers'] as const)('waits for unresolved %s before showing cards', async (source) => {
    const data = source === 'providers' ? mocks.providerData : mocks.templateData
    const loading = source === 'providers' ? mocks.providersLoading : mocks.templatesLoading
    data.value = undefined
    loading.value = true
    const { app, root } = await mountPage()

    expect(root.querySelector('[data-slot="skeleton"]')).not.toBeNull()
    expect(root.querySelector('[data-provider]')).toBeNull()
    data.value = [{ id: 'loaded', name: 'Loaded' }]
    loading.value = false
    await nextTick()
    expect(root.querySelector('[data-slot="skeleton"]')).toBeNull()
    expect(root.querySelector('[data-provider]')).not.toBeNull()
    app.unmount()
  })

  it('leaves the skeleton when an initial request fails without data', async () => {
    mocks.providerData.value = undefined
    mocks.providersLoading.value = true
    const { app, root } = await mountPage()
    expect(root.querySelector('[data-slot="skeleton"]')).not.toBeNull()

    // A rejected query returns to idle and keeps its data undefined.
    mocks.providersLoading.value = false
    await nextTick()
    expect(root.querySelector('[data-slot="skeleton"]')).toBeNull()
    expect(cardNames(root)).toContain('302.AI')
    app.unmount()
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

  it('opens a source as a draft detail without opening the add dialog', async () => {
    const { app, root } = await mountPage('anthropic')

    ;(root.querySelector('[data-provider="Anthropic"]') as HTMLButtonElement).click()
    await nextTick()

    expect(mocks.replace).toHaveBeenCalledWith({ query: { provider: 'source:anthropic' } })
    expect(root.querySelector('[data-testid="provider-detail"]')?.textContent).toBe('undefined:Anthropic')
    expect(root.querySelector('[data-testid="add-provider-dialog"]')).toBeNull()
    app.unmount()
  })

  it('moves the route to the provider once a draft is created', async () => {
    const { app, root } = await mountPage('anthropic')

    ;(root.querySelector('[data-provider="Anthropic"]') as HTMLButtonElement).click()
    await nextTick()
    ;(root.querySelector('[data-testid="materialize-provider"]') as HTMLButtonElement).click()
    await nextTick()

    expect(mocks.replace).toHaveBeenLastCalledWith({ query: { provider: 'provider-anthropic' } })
    expect(mocks.replace).not.toHaveBeenCalledWith({ query: {} })
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

  it('offers models.dev sources after the configured providers, a page at a time', async () => {
    const { app, root } = await mountPage()

    const names = cardNames(root)
    expect(names).toHaveLength(20)
    expect(names.slice(0, 5)).toEqual(['One', 'Two', 'OpenAI Codex', 'GitHub Copilot', 'OpenCode Go'])
    expect(names).toContain('above.dev')
    expect(sectionTitles(root)).toEqual([
      'provider.gallery.configured',
      'provider.gallery.subscriptions',
      'provider.gallery.others',
    ])
    const pagination = root.querySelector('[data-testid="pagination"]')
    expect(Number(pagination?.getAttribute('data-pages'))).toBeGreaterThan(5)

    ;(root.querySelector('[data-testid="next-page"]') as HTMLButtonElement).click()
    await nextTick()
    expect(cardNames(root)).not.toContain('One')
    expect(cardNames(root)).toHaveLength(20)
    // The second page continues the other providers under their title again.
    expect(sectionTitles(root)).toEqual(['provider.gallery.others'])
    app.unmount()
  })

  it('leaves out entries a provider already uses, by any route it was made', async () => {
    mocks.providerData.value = [
      { id: 'from-source', name: 'My DeepSeek', enable: true, metadata: { catalog: 'deepseek', source: 'deepseek' } },
      { id: 'from-template', name: 'My Groq', enable: true, metadata: { registry: { source: 'groq.yaml' } } },
      { id: 'from-preset', name: 'My Mistral', enable: true, metadata: { preset: { source: 'mistral.yaml' } } },
      { id: 'from-entry', name: 'My Ollama', enable: true, metadata: { catalog: 'ollama' } },
    ]
    const { app, root } = await mountPage('')
    const all: string[] = []
    let guard = 0
    while (guard++ < 20) {
      all.push(...cardNames(root).map(String))
      const pagination = root.querySelector('[data-testid="pagination"]')
      if (Number(pagination?.getAttribute('data-page')) >= Number(pagination?.getAttribute('data-pages'))) break
      ;(root.querySelector('[data-testid="next-page"]') as HTMLButtonElement).click()
      await nextTick()
    }
    for (const name of ['DeepSeek', 'Groq', 'Mistral', 'Ollama']) expect(all).not.toContain(name)
    expect(all).toContain('OpenAI')
    expect(all).toContain('OpenAI Codex')
    app.unmount()
  })

  it('filters sources by search and returns to the first page', async () => {
    const { app, root, query } = await mountPage()
    ;(root.querySelector('[data-testid="next-page"]') as HTMLButtonElement).click()
    await nextTick()

    query.value = 'deepseek'
    await nextTick()
    expect(cardNames(root)).toEqual(['DeepSeek'])
    expect(sectionTitles(root)).toEqual(['provider.gallery.others'])
    expect(root.querySelector('[data-testid="pagination"]')).toBeNull()

    // A section shows only while the search leaves it a card.
    query.value = 'copilot'
    await nextTick()
    expect(cardNames(root)).toEqual(['GitHub Copilot'])
    expect(sectionTitles(root)).toEqual(['provider.gallery.subscriptions'])

    query.value = 'zzzz'
    await nextTick()
    expect(cardNames(root)).toEqual([])
    expect(root.textContent).toContain('provider.searchNoResults')
    app.unmount()
  })

  it('keeps offering a subscription when a provider of its type points elsewhere', async () => {
    mocks.providerData.value = [
      { id: 'proxy-go', name: 'Go Proxy', enable: true, client_type: 'opencode-go', config: { base_url: 'https://proxy.example.com/v1' }, metadata: {} },
    ]
    const { app, root } = await mountPage('')
    expect(sectionTitles(root).slice(0, 2)).toEqual(['provider.gallery.configured', 'provider.gallery.subscriptions'])
    expect(cardNames(root).slice(0, 4)).toEqual(['Go Proxy', 'OpenAI Codex', 'GitHub Copilot', 'OpenCode Go'])
    app.unmount()
  })

  it('counts a subscription as configured when its client type was used in the custom form', async () => {
    mocks.providerData.value = [
      { id: 'custom-codex', name: 'My Codex', enable: true, client_type: 'openai-codex', metadata: {} },
    ]
    const { app, root } = await mountPage('')
    expect(sectionTitles(root).slice(0, 2)).toEqual(['provider.gallery.configured', 'provider.gallery.subscriptions'])
    expect(cardNames(root).slice(0, 3)).toEqual(['My Codex', 'GitHub Copilot', 'OpenCode Go'])
    app.unmount()
  })
})
