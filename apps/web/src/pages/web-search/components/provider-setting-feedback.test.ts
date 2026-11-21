// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, provide, ref, type App } from 'vue'
import type { FetchprovidersGetResponse, SearchprovidersGetResponse } from '@memohai/sdk'

const DEFAULT_SEARCH_META = [
  {
    provider: 'brave',
    display_name: 'Brave',
    config_schema: {
      fields: {
        api_key: { type: 'string', secret: true, required: true, title: 'API Key' },
        base_url: { type: 'string', title: 'Base URL', example: 'https://api.search.brave.com/res/v1/web/search' },
        timeout_seconds: { type: 'number', title: 'Timeout (seconds)', example: 15 },
      },
    },
  },
  {
    provider: 'searxng',
    display_name: 'SearXNG',
    config_schema: {
      fields: {
        base_url: { type: 'string', required: true, title: 'Base URL', example: 'http://localhost:8080/search' },
        headers: { type: 'object', title: 'HTTP headers' },
        timeout_seconds: { type: 'number', title: 'Timeout (seconds)', example: 15 },
      },
    },
  },
]

const DEFAULT_FETCH_META = [
  {
    provider: 'jina',
    display_name: 'Jina Reader',
    config_schema: {
      fields: {
        api_key: { type: 'string', secret: true, title: 'API Key' },
        base_url: { type: 'string', title: 'Base URL', example: 'https://r.jina.ai/' },
        timeout_seconds: { type: 'number', title: 'Timeout (seconds)', example: 30 },
      },
    },
  },
  {
    provider: 'cloudflare_markdown',
    display_name: 'Cloudflare Markdown',
    config_schema: {
      fields: {
        account_id: { type: 'string', required: true, title: 'Account ID' },
        api_token: { type: 'string', secret: true, required: true, title: 'API Token' },
        base_url: { type: 'string', title: 'Base URL', example: 'https://api.cloudflare.com/client/v4' },
        timeout_seconds: { type: 'number', title: 'Timeout (seconds)', example: 30 },
      },
    },
  },
]

const mocks = vi.hoisted(() => ({
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
  invalidateQueries: vi.fn(),
  postSearchProviders: vi.fn(),
  putSearchProvidersById: vi.fn(),
  deleteSearchProvidersById: vi.fn(),
  postFetchProviders: vi.fn(),
  putFetchProvidersById: vi.fn(),
  deleteFetchProvidersById: vi.fn(),
  searchMeta: [] as unknown[],
  fetchMeta: [] as unknown[],
  searchProvidersList: [] as unknown[],
  fetchProvidersList: [] as unknown[],
  route: { query: {} as Record<string, string> },
  replace: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => mocks.route,
  useRouter: () => ({ replace: mocks.replace }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, fallbackOrParams?: string | Record<string, unknown>) =>
      typeof fallbackOrParams === 'string' ? fallbackOrParams : key,
  }),
}))

vi.mock('@/utils/api-error', () => ({
  resolveApiErrorMessage: (error: unknown, fallback: string) => {
    if (error instanceof Error && error.message.startsWith('API:')) return error.message
    return fallback
  },
}))

vi.mock('@pinia/colada', async () => {
  const { ref } = await import('vue')
  const searchListRef = ref<unknown[]>([])
  const fetchListRef = ref<unknown[]>([])
  return {
    useQuery: ({ key }: { key: () => string[] }) => {
      switch (key()[0]) {
        case 'search-providers-meta':
          return { data: ref(mocks.searchMeta), isLoading: ref(false), error: ref(null), refetch: vi.fn() }
        case 'fetch-providers-meta':
          return { data: ref(mocks.fetchMeta), isLoading: ref(false), error: ref(null), refetch: vi.fn() }
        case 'search-providers':
          searchListRef.value = mocks.searchProvidersList
          return { data: searchListRef, isLoading: ref(false), error: ref(null), refetch: vi.fn() }
        case 'fetch-providers':
          fetchListRef.value = mocks.fetchProvidersList
          return { data: fetchListRef, isLoading: ref(false), error: ref(null), refetch: vi.fn() }
        default:
          return { data: ref([]), isLoading: ref(false), error: ref(null), refetch: vi.fn() }
      }
    },
    useMutation: (options: {
      mutation: (...args: unknown[]) => Promise<unknown>
      onSuccess?: (data: unknown) => void
      onError?: (error: unknown) => void
      onSettled?: () => void
    }) => ({
      isLoading: ref(false),
      mutate: async (...args: unknown[]) => {
        try {
          const result = await options.mutation(...args)
          options.onSuccess?.(result)
          return result
        } catch (error) {
          options.onError?.(error)
          return undefined
        } finally {
          options.onSettled?.()
        }
      },
    }),
    useQueryCache: () => ({
      invalidateQueries: mocks.invalidateQueries,
      getQueryData: (key: string[]) => {
        if (key[0] === 'search-providers') return searchListRef.value
        if (key[0] === 'fetch-providers') return fetchListRef.value
        return undefined
      },
      setQueryData: (key: string[], updater: unknown) => {
        if (key[0] === 'search-providers') {
          const next = typeof updater === 'function' ? updater(searchListRef.value) : updater
          mocks.searchProvidersList = next as unknown[]
          searchListRef.value = next as unknown[]
        } else if (key[0] === 'fetch-providers') {
          const next = typeof updater === 'function' ? updater(fetchListRef.value) : updater
          mocks.fetchProvidersList = next as unknown[]
          fetchListRef.value = next as unknown[]
        }
      },
    }),
  }
})

vi.mock('@memohai/sdk', () => ({
  getSearchProviders: vi.fn(),
  getSearchProvidersMeta: vi.fn(),
  postSearchProviders: mocks.postSearchProviders,
  putSearchProvidersById: mocks.putSearchProvidersById,
  deleteSearchProvidersById: mocks.deleteSearchProvidersById,
  getFetchProviders: vi.fn(),
  getFetchProvidersMeta: vi.fn(),
  postFetchProviders: mocks.postFetchProviders,
  putFetchProvidersById: mocks.putFetchProvidersById,
  deleteFetchProvidersById: mocks.deleteFetchProvidersById,
}))

vi.mock('@/components/provider-identity-card/index.vue', () => ({
  default: defineComponent({
    props: { name: { type: String, default: '' } },
    setup: (props, { slots }) => () => h('div', { 'data-testid': 'identity-card', 'data-name': props.name }, [
      slots.media?.(),
      slots.actions?.(),
    ]),
  }),
}))

vi.mock('@/components/search-provider-logo/index.vue', () => ({
  default: defineComponent({ render: () => h('span') }),
}))

vi.mock('@/components/loading-button/index.vue', () => ({
  default: defineComponent({
    inheritAttrs: false,
    setup: (_, { attrs, slots }) => () => h('button', { ...attrs, 'data-testid': 'save-button' }, slots.default?.()),
  }),
}))

vi.mock('@felinic/ui', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@felinic/ui')>()
  const { defineComponent: define, h: el } = await import('vue')
  const Switch = define({
    inheritAttrs: false,
    props: { modelValue: { type: Boolean, default: false }, disabled: { type: Boolean, default: false } },
    emits: ['update:modelValue'],
    setup: (props, { emit }) => () => el('button', {
      'type': 'button',
      'data-testid': 'enable-switch',
      'data-checked': String(props.modelValue),
      'disabled': props.disabled,
      'onClick': () => emit('update:modelValue', !props.modelValue),
    }),
  })
  const ConfirmPopover = define({
    emits: ['confirm'],
    setup: (_, { slots, emit }) => () => el('div', { 'data-testid': 'delete-popover' }, [
      slots.trigger?.(),
      el('button', {
        'type': 'button',
        'data-testid': 'delete-confirm',
        'onClick': () => emit('confirm'),
      }, 'confirm'),
    ]),
  })
  return {
    ...actual,
    ConfirmPopover,
    Switch,
    toast: {
      success: mocks.toastSuccess,
      error: mocks.toastError,
    },
  }
})

let app: App | undefined
let root: HTMLDivElement

async function flush() {
  await new Promise(resolve => setTimeout(resolve, 0))
  for (let i = 0; i < 6; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

async function submitForm() {
  root.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await new Promise(resolve => setTimeout(resolve, 20))
  await flush()
}

function mountSearch(
  initial: SearchprovidersGetResponse,
  handlers: {
    onMaterialized?: (provider: SearchprovidersGetResponse) => void
  } = {},
) {
  const cur = ref<SearchprovidersGetResponse | undefined>(initial)
  return import('./provider-setting.vue').then(({ default: ProviderSetting }) => {
    const Host = defineComponent({
      setup() {
        provide('curSearchProvider', cur)
        return () => h(ProviderSetting, {
          onMaterialized: handlers.onMaterialized,
        })
      },
    })
    root = document.createElement('div')
    document.body.appendChild(root)
    app = createApp(Host)
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    return cur
  })
}

function mountFetch(
  initial: FetchprovidersGetResponse,
  handlers: {
    onMaterialized?: (provider: FetchprovidersGetResponse) => void
  } = {},
) {
  const cur = ref<FetchprovidersGetResponse | undefined>(initial)
  return import('./fetch-provider-setting.vue').then(({ default: FetchProviderSetting }) => {
    const Host = defineComponent({
      setup() {
        provide('curFetchProvider', cur)
        return () => h(FetchProviderSetting, {
          onMaterialized: handlers.onMaterialized,
        })
      },
    })
    root = document.createElement('div')
    document.body.appendChild(root)
    app = createApp(Host)
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    return cur
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.stubGlobal('ResizeObserver', class {
    observe() {}
    unobserve() {}
    disconnect() {}
  })
  mocks.searchMeta = [...DEFAULT_SEARCH_META]
  mocks.fetchMeta = [...DEFAULT_FETCH_META]
  mocks.searchProvidersList = []
  mocks.fetchProvidersList = []
  mocks.route.query = {}
})

afterEach(() => {
  app?.unmount()
  root?.remove()
  app = undefined
})

describe('search provider mutation feedback & delete reset', () => {
  it('shows saveSuccess toast and syncs materialized state when saving creates or updates a search provider', async () => {
    const onMaterialized = vi.fn()
    mocks.postSearchProviders.mockResolvedValueOnce({
      data: { id: 's-1', name: 'Brave', provider: 'brave', enable: false, config: { api_key: 'sk-1' } },
    })
    const cur = await mountSearch({
      name: 'Brave',
      provider: 'brave',
      enable: false,
      config: { api_key: 'sk-1' },
    }, { onMaterialized })
    await flush()

    await submitForm()

    expect(mocks.postSearchProviders).toHaveBeenCalledTimes(1)
    expect(mocks.toastSuccess).toHaveBeenCalledWith('webSearch.saveSuccess')
    expect(cur.value?.id).toBe('s-1')

    mocks.toastSuccess.mockClear()
    onMaterialized.mockClear()
    const updatedRecord = { id: 's-1', name: 'Brave Pro', provider: 'brave', enable: true, config: { api_key: 'sk-2' } }
    mocks.putSearchProvidersById.mockResolvedValueOnce({
      data: updatedRecord,
    })

    await submitForm()

    expect(mocks.putSearchProvidersById).toHaveBeenCalledTimes(1)
    expect(mocks.toastSuccess).toHaveBeenCalledWith('webSearch.saveSuccess')
    expect(cur.value).toEqual(updatedRecord)
    expect(onMaterialized).toHaveBeenCalledWith(updatedRecord)
  })

  it('surfaces localized API errors via toast.error when search provider save or enable toggle fails', async () => {
    mocks.putSearchProvidersById.mockRejectedValueOnce(new Error('API:Invalid API key'))
    const cur = await mountSearch({
      id: 's-1',
      name: 'Brave',
      provider: 'brave',
      enable: false,
      config: { api_key: 'bad' },
    })
    await flush()

    await submitForm()

    expect(mocks.toastError).toHaveBeenCalledWith('API:Invalid API key')
    expect(mocks.toastSuccess).not.toHaveBeenCalled()

    mocks.toastError.mockClear()
    mocks.putSearchProvidersById.mockRejectedValueOnce(new Error('API:Quota exceeded'))

    ;(root.querySelector('[data-testid="enable-switch"]') as HTMLButtonElement).click()
    await flush()

    expect(cur.value?.enable).toBe(false)
    expect(mocks.toastError).toHaveBeenCalledWith('API:Quota exceeded')
  })

  it('shows deleteSuccess toast and cleanly resets persisted SearXNG fields + secret headers back to an unmaterialized built-in entry', async () => {
    mocks.deleteSearchProvidersById.mockResolvedValueOnce({ data: undefined })
    const onMaterialized = vi.fn()

    const cur = await mountSearch({
      id: 's-searx',
      name: 'Internal SearXNG',
      provider: 'searxng',
      enable: true,
      config: {
        base_url: 'https://searx.internal/search',
        headers: {
          Authorization: 'Bearer top-secret',
          'X-Tenant': 'acme',
        },
      },
    }, { onMaterialized })
    await flush()

    expect(root.querySelector<HTMLInputElement>('#search-provider-base_url')?.value).toBe('https://searx.internal/search')
    expect(
      [...root.querySelectorAll<HTMLInputElement>('input[aria-label="webSearch.headerValue"]')].map(i => i.value),
    ).toEqual(['Bearer top-secret', 'acme'])

    ;(root.querySelector('[data-testid="delete-confirm"]') as HTMLButtonElement).click()
    await flush()

    expect(mocks.deleteSearchProvidersById).toHaveBeenCalledWith({
      path: { id: 's-searx' },
      throwOnError: true,
    })
    expect(mocks.toastSuccess).toHaveBeenCalledWith('webSearch.deleteSuccess')
    expect(onMaterialized).toHaveBeenCalledWith(expect.objectContaining({ provider: 'searxng', enable: false }))
    expect(cur.value?.id).toBeUndefined()
    expect(cur.value?.enable).toBe(false)
    expect(cur.value?.name).toBe('SearXNG')
    expect(root.querySelector('[data-testid="delete-popover"]')).toBeNull()
    expect(root.querySelector('[data-testid="enable-switch"]')?.getAttribute('data-checked')).toBe('false')
    expect(root.querySelector<HTMLInputElement>('#search-provider-base_url')?.value).toBe('')
    expect(
      [...root.querySelectorAll<HTMLInputElement>('input[aria-label="webSearch.headerValue"]')].map(i => i.value),
    ).toEqual([])
  })

  it('surfaces localized API errors and preserves state when deleting a search provider fails', async () => {
    mocks.deleteSearchProvidersById.mockRejectedValueOnce(new Error('API:Provider in use'))

    const cur = await mountSearch({
      id: 's-1',
      name: 'Brave',
      provider: 'brave',
      enable: true,
      config: { api_key: 'keep-me' },
    })
    await flush()

    ;(root.querySelector('[data-testid="delete-confirm"]') as HTMLButtonElement).click()
    await flush()

    expect(mocks.toastError).toHaveBeenCalledWith('API:Provider in use')
    expect(mocks.toastSuccess).not.toHaveBeenCalled()
    expect(cur.value?.id).toBe('s-1')
    expect(root.querySelector<HTMLInputElement>('#search-provider-api_key')?.value).toBe('keep-me')
  })
})

describe('fetch provider mutation feedback & delete reset', () => {
  it('shows saveFetchSuccess toast on create/update and localized error toast on save/toggle failure', async () => {
    mocks.postFetchProviders.mockResolvedValueOnce({
      data: { id: 'f-1', name: 'Jina Reader', provider: 'jina', enable: false, config: { api_key: 'jina-1' } },
    })
    const cur = await mountFetch({
      name: 'Jina Reader',
      provider: 'jina',
      enable: false,
      config: { api_key: 'jina-1' },
    })
    await flush()

    await submitForm()

    expect(mocks.postFetchProviders).toHaveBeenCalledTimes(1)
    expect(mocks.toastSuccess).toHaveBeenCalledWith('webSearch.saveFetchSuccess')
    expect(cur.value?.id).toBe('f-1')

    mocks.toastError.mockClear()
    mocks.putFetchProvidersById.mockRejectedValueOnce(new Error('API:Bad token'))
    await submitForm()

    expect(mocks.toastError).toHaveBeenCalledWith('API:Bad token')

    mocks.toastError.mockClear()
    mocks.putFetchProvidersById.mockRejectedValueOnce(new Error('API:Toggle rejected'))
    ;(root.querySelector('[data-testid="enable-switch"]') as HTMLButtonElement).click()
    await flush()

    expect(cur.value?.enable).toBe(false)
    expect(mocks.toastError).toHaveBeenCalledWith('API:Toggle rejected')
  })

  it('shows deleteFetchSuccess toast and resets persisted fetch provider fields back to an unmaterialized built-in entry', async () => {
    mocks.deleteFetchProvidersById.mockResolvedValueOnce({ data: undefined })
    const onMaterialized = vi.fn()

    const cur = await mountFetch({
      id: 'f-cf',
      name: 'My Cloudflare',
      provider: 'cloudflare_markdown',
      enable: true,
      config: { account_id: 'acc-1', api_token: 'cf-secret' },
    }, { onMaterialized })
    await flush()

    expect(root.querySelector<HTMLInputElement>('#fetch-provider-api_token')?.value).toBe('cf-secret')

    ;(root.querySelector('[data-testid="delete-confirm"]') as HTMLButtonElement).click()
    await flush()

    expect(mocks.deleteFetchProvidersById).toHaveBeenCalledWith({
      path: { id: 'f-cf' },
      throwOnError: true,
    })
    expect(mocks.toastSuccess).toHaveBeenCalledWith('webSearch.deleteFetchSuccess')
    expect(onMaterialized).toHaveBeenCalledWith(expect.objectContaining({ provider: 'cloudflare_markdown', enable: false }))
    expect(cur.value?.id).toBeUndefined()
    expect(cur.value?.enable).toBe(false)
    expect(cur.value?.name).toBe('Cloudflare Markdown')
    expect(root.querySelector('[data-testid="delete-popover"]')).toBeNull()
    expect(root.querySelector<HTMLInputElement>('#fetch-provider-api_token')?.value).toBe('')
  })

  it('surfaces localized API errors (with webSearch.deleteFetchFailed fallback) when deleting a fetch provider fails', async () => {
    mocks.deleteFetchProvidersById.mockRejectedValueOnce(new Error('network glitch'))

    const cur = await mountFetch({
      id: 'f-cf',
      name: 'My Cloudflare',
      provider: 'cloudflare_markdown',
      enable: true,
      config: { account_id: 'acc-1', api_token: 'cf-secret' },
    })
    await flush()

    ;(root.querySelector('[data-testid="delete-confirm"]') as HTMLButtonElement).click()
    await flush()

    expect(mocks.toastError).toHaveBeenCalledWith('webSearch.deleteFetchFailed')
    expect(cur.value?.id).toBe('f-cf')
  })

  it('clears optimistic materialized entries in web-search/index.vue when a newly created provider is deleted', async () => {
    mocks.route.query = { webProvider: 'search:brave' }
    mocks.postSearchProviders.mockResolvedValueOnce({
      data: { id: 's-created', name: 'Brave', provider: 'brave', enable: false, config: { api_key: 'sk-created' } },
    })
    mocks.deleteSearchProvidersById.mockResolvedValueOnce({ data: undefined })

    const { default: WebSearchPage } = await import('../index.vue')
    root = document.createElement('div')
    document.body.appendChild(root)
    app = createApp({ render: () => h(WebSearchPage, { searchQuery: '' }) })
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    await flush()

    // Materialize via Save
    const apiKeyInput = root.querySelector<HTMLInputElement>('#search-provider-api_key')!
    apiKeyInput.value = 'sk-created'
    apiKeyInput.dispatchEvent(new Event('input'))
    await flush()
    await submitForm()

    expect(root.querySelector('[data-testid="delete-popover"]')).not.toBeNull()

    // Now delete the newly materialized record: optimisticSearchProviders must drop it
    // so the detail view reverts to an unmaterialized entry (no delete button, empty key).
    ;(root.querySelector('[data-testid="delete-confirm"]') as HTMLButtonElement).click()
    await flush()

    expect(root.querySelector('[data-testid="delete-popover"]')).toBeNull()
    expect(root.querySelector<HTMLInputElement>('#search-provider-api_key')?.value).toBe('')
  })

  it('prevents stale list query data from resurrecting a deleted provider before refetch completes', async () => {
    mocks.route.query = { webProvider: 'search:brave' }
    mocks.searchProvidersList = [
      { id: 's-stale', name: 'Stale Brave', provider: 'brave', enable: true, config: { api_key: 'stale-secret' } },
    ]
    mocks.deleteSearchProvidersById.mockResolvedValueOnce({ data: undefined })

    const { default: WebSearchPage } = await import('../index.vue')
    root = document.createElement('div')
    document.body.appendChild(root)
    app = createApp({ render: () => h(WebSearchPage, { searchQuery: '' }) })
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    await flush()

    expect(root.querySelector('[data-testid="delete-popover"]')).not.toBeNull()
    expect(root.querySelector<HTMLInputElement>('#search-provider-api_key')?.value).toBe('stale-secret')

    // Delete succeeds while searchProvidersList still holds the pre-refetch stale record.
    ;(root.querySelector('[data-testid="delete-confirm"]') as HTMLButtonElement).click()
    await flush()

    expect(mocks.toastSuccess).toHaveBeenCalledWith('webSearch.deleteSuccess')
    expect(root.querySelector('[data-testid="delete-popover"]')).toBeNull()
    expect(root.querySelector('[data-testid="enable-switch"]')?.getAttribute('data-checked')).toBe('false')
    expect(root.querySelector<HTMLInputElement>('#search-provider-api_key')?.value).toBe('')
  })

  it('does not hijack the active detail pane or wipe drafts when switching providers before an in-flight mutation finishes', async () => {
    mocks.route.query = { webProvider: 'search:brave' }
    mocks.searchProvidersList = [
      { id: 's-brave', name: 'Brave', provider: 'brave', enable: true, config: { api_key: 'brave-key' } },
      { id: 's-searx', name: 'SearXNG', provider: 'searxng', enable: true, config: { base_url: 'https://searx.example/search' } },
    ]
    let resolveDelete!: (val: unknown) => void
    mocks.deleteSearchProvidersById.mockImplementationOnce(() => new Promise((resolve) => {
      resolveDelete = resolve
    }))

    const { reactive } = await import('vue')
    mocks.route = reactive({ query: { webProvider: 'search:brave' } as Record<string, string> })
    const { default: WebSearchPage } = await import('../index.vue')
    root = document.createElement('div')
    document.body.appendChild(root)
    app = createApp({ render: () => h(WebSearchPage, { searchQuery: '' }) })
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    await flush()

    ;(root.querySelector('[data-testid="delete-confirm"]') as HTMLButtonElement).click()
    await vi.waitFor(() => expect(mocks.deleteSearchProvidersById).toHaveBeenCalledOnce())

    // Navigate to SearXNG and start editing before Brave's DELETE finishes.
    mocks.route.query = { webProvider: 'search:searxng' }
    await flush()
    const baseUrlInput = root.querySelector<HTMLInputElement>('#search-provider-base_url')!
    expect(baseUrlInput.value).toBe('https://searx.example/search')
    baseUrlInput.value = 'https://draft.searx.example/search'
    baseUrlInput.dispatchEvent(new Event('input'))
    await flush()

    resolveDelete({ data: undefined })
    await flush()

    // Active pane stays on SearXNG with the user's draft intact; URL is not rewritten back to Brave.
    expect(mocks.replace).not.toHaveBeenCalledWith(expect.objectContaining({
      query: expect.objectContaining({ webProvider: 'search:brave' }),
    }))
    expect(root.querySelector<HTMLInputElement>('#search-provider-base_url')?.value).toBe('https://draft.searx.example/search')
  })
})
