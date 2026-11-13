// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as sdk from '@memohai/sdk'
import { createApp, defineComponent, h, nextTick, provide, ref, type App, type Component, type Ref } from 'vue'
import { createPinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import ProviderSetting from './provider-setting.vue'
import FetchProviderSetting from './fetch-provider-setting.vue'

const { SEARCH_META_FIXTURES, FETCH_META_FIXTURES } = vi.hoisted(() => ({
  SEARCH_META_FIXTURES: [
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
      provider: 'tavily',
      display_name: 'Tavily',
      config_schema: {
        fields: {
          api_key: { type: 'string', secret: true, required: true, title: 'API Key' },
          base_url: { type: 'string', title: 'Base URL', example: 'https://api.tavily.com/search' },
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
          language: { type: 'string', title: 'Language', example: 'all' },
          safesearch: { type: 'number', title: 'SafeSearch', example: 1 },
          categories: { type: 'string', title: 'Categories', example: 'general' },
          timeout_seconds: { type: 'number', title: 'Timeout (seconds)', example: 15 },
        },
      },
    },
  ],
  FETCH_META_FIXTURES: [
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
  ],
}))

vi.mock('@memohai/sdk', () => ({
  getSearchProvidersMeta: vi.fn().mockResolvedValue({ data: SEARCH_META_FIXTURES }),
  getFetchProvidersMeta: vi.fn().mockResolvedValue({ data: FETCH_META_FIXTURES }),
  postSearchProviders: vi.fn(),
  putSearchProvidersById: vi.fn(),
  deleteSearchProvidersById: vi.fn(),
  postFetchProviders: vi.fn(),
  putFetchProvidersById: vi.fn(),
  deleteFetchProvidersById: vi.fn(),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })

type Provider = { id?: string, name?: string, provider?: string, enable?: boolean, config?: Record<string, unknown> }

let app: App | null = null
afterEach(() => {
  app?.unmount()
  app = null
  document.body.innerHTML = ''
})

async function flush() {
  for (let i = 0; i < 5; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

async function mountSetting(component: Component, injectionKey: string, initial: Provider) {
  const current = ref<Provider | undefined>(initial) as Ref<Provider | undefined>
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(defineComponent({
    setup() {
      provide(injectionKey, current)
      return () => h(component)
    },
  }))
  app.use(createPinia()).use(PiniaColada)
  app.config.globalProperties.$t = (key: string) => key
  app.mount(root)
  await flush()
  return { root, current }
}

function inputWithValue(root: HTMLElement, value: string) {
  return [...root.querySelectorAll('input')].find(input => input.value === value)
}

async function type(input: HTMLInputElement, value: string) {
  input.value = value
  input.dispatchEvent(new Event('input', { bubbles: true }))
  input.dispatchEvent(new Event('change', { bubbles: true }))
  await flush()
}

const cases = [
  {
    label: 'search provider',
    component: ProviderSetting,
    key: 'curSearchProvider',
    provider: { id: 'p1', name: 'Brave', provider: 'brave', enable: false, config: { api_key: 'saved-key' } },
    other: { id: 'p2', name: 'Tavily', provider: 'tavily', enable: false, config: { api_key: 'tavily-key' } },
    otherSecret: 'tavily-key',
    otherDefaultUrl: 'https://api.tavily.com/search',
  },
  {
    label: 'fetch provider',
    component: FetchProviderSetting,
    key: 'curFetchProvider',
    provider: { id: 'f1', name: 'Jina Reader', provider: 'jina', enable: false, config: { api_key: 'saved-key' } },
    other: { id: 'f2', name: 'Cloudflare', provider: 'cloudflare_markdown', enable: false, config: { account_id: 'acc-1', api_token: 'cf-token' } },
    otherSecret: 'cf-token',
    otherDefaultUrl: 'https://api.cloudflare.com/client/v4',
  },
] as const

describe.each(cases)('$label settings form', ({ component, key, provider, other, otherSecret, otherDefaultUrl }) => {
  it('keeps unsaved edits when the same provider is handed back as a new object', async () => {
    const { root, current } = await mountSetting(component, key, { ...provider })
    await type(inputWithValue(root, 'saved-key')!, 'typed-key')
    await type(inputWithValue(root, provider.name)!, 'Typed name')

    // What a list refetch or the enable switch does: same data, new object.
    current.value = { ...provider, enable: true, config: { ...provider.config } }
    await flush()

    expect(inputWithValue(root, 'typed-key')).toBeTruthy()
    expect(inputWithValue(root, 'Typed name')).toBeTruthy()
  })

  it('lets untouched fields follow the server while an edited one is kept', async () => {
    const { root, current } = await mountSetting(component, key, { ...provider })
    await type(inputWithValue(root, 'saved-key')!, 'typed-key')

    current.value = { ...provider, name: 'Renamed elsewhere', config: { ...provider.config, base_url: 'https://mirror.example/search' } }
    await flush()

    expect(inputWithValue(root, 'typed-key')).toBeTruthy()
    expect(inputWithValue(root, 'Renamed elsewhere')).toBeTruthy()
    expect(inputWithValue(root, 'https://mirror.example/search')).toBeTruthy()
  })

  it('replaces the form cleanly when a different provider is selected without outgoing child pollution', async () => {
    const { root, current } = await mountSetting(component, key, { ...provider })
    await type(inputWithValue(root, 'saved-key')!, 'typed-key')

    current.value = { ...other, config: { ...other.config } }
    await flush()

    expect(inputWithValue(root, 'typed-key')).toBeUndefined()
    expect(inputWithValue(root, other.name)).toBeTruthy()
    expect(inputWithValue(root, otherSecret)).toBeTruthy()
    expect(inputWithValue(root, otherDefaultUrl)).toBeTruthy()
  })

  it('saves what is on screen', async () => {
    const put = vi.mocked(key === 'curSearchProvider' ? sdk.putSearchProvidersById : sdk.putFetchProvidersById)
    put.mockResolvedValue({ data: {} } as never)
    const { root } = await mountSetting(component, key, { ...provider })
    await type(inputWithValue(root, 'saved-key')!, 'typed-key')
    root.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await vi.waitFor(() => expect(put).toHaveBeenCalled())
    const body = (put.mock.calls.at(-1)![0] as { body: { name: string, config: Record<string, unknown> } }).body
    expect(body.name).toBe(provider.name)
    expect(body.config.api_key).toBe('typed-key')
  })

  it('keeps the form filled while the pane leaves after the selection is cleared', async () => {
    const { root, current } = await mountSetting(component, key, { ...provider })

    current.value = undefined
    await flush()

    expect(inputWithValue(root, 'saved-key')).toBeTruthy()
    expect(inputWithValue(root, provider.name)).toBeTruthy()
  })
})

describe('cross-provider switching and placeholder hydration', () => {
  it('switches cleanly between SearXNG and Brave without losing headers or API keys', async () => {
    const searxng: Provider = {
      id: 's1',
      name: 'SearXNG',
      provider: 'searxng',
      enable: true,
      config: { base_url: 'https://searx.example/search', headers: { Authorization: 'Bearer searx-secret' } },
    }
    const brave: Provider = {
      id: 'p1',
      name: 'Brave',
      provider: 'brave',
      enable: true,
      config: { api_key: 'brave-secret' },
    }

    const { root, current } = await mountSetting(ProviderSetting, 'curSearchProvider', searxng)
    expect(inputWithValue(root, 'https://searx.example/search')).toBeTruthy()
    expect(inputWithValue(root, 'Bearer searx-secret')).toBeTruthy()

    current.value = { ...brave, config: { ...brave.config } }
    await flush()
    expect(inputWithValue(root, 'brave-secret')).toBeTruthy()
    expect(inputWithValue(root, 'https://api.search.brave.com/res/v1/web/search')).toBeTruthy()

    current.value = { ...searxng, config: { ...searxng.config, headers: { Authorization: 'Bearer searx-secret' } } }
    await flush()
    expect(inputWithValue(root, 'https://searx.example/search')).toBeTruthy()
    expect(inputWithValue(root, 'Bearer searx-secret')).toBeTruthy()
  })

  it('hydrates server base_url and secret headers when a SearXNG placeholder resolves to a persisted record', async () => {
    const { root, current } = await mountSetting(ProviderSetting, 'curSearchProvider', {
      name: 'SearXNG',
      provider: 'searxng',
      enable: false,
      config: {},
    })

    // Meta schema defaults arrive while the list query is still in flight.
    current.value = {
      name: 'SearXNG',
      provider: 'searxng',
      enable: false,
      config: { language: 'all', safesearch: 1, categories: 'general', timeout_seconds: 15 },
    }
    await flush()

    // Persisted server record arrives with real URL and secret headers.
    current.value = {
      id: 's1',
      name: 'Prod SearXNG',
      provider: 'searxng',
      enable: true,
      config: { base_url: 'https://searx.example/search', headers: { Authorization: 'Bearer secret' } },
    }
    await flush()

    expect(inputWithValue(root, 'Prod SearXNG')).toBeTruthy()
    expect(inputWithValue(root, 'https://searx.example/search')).toBeTruthy()
    expect(inputWithValue(root, 'Bearer secret')).toBeTruthy()
  })

  it('hydrates server credentials when a fetch provider placeholder resolves to a persisted record', async () => {
    const { root, current } = await mountSetting(FetchProviderSetting, 'curFetchProvider', {
      name: 'Cloudflare Markdown',
      provider: 'cloudflare_markdown',
      enable: false,
      config: {},
    })

    current.value = {
      name: 'Cloudflare Markdown',
      provider: 'cloudflare_markdown',
      enable: false,
      config: { base_url: 'https://api.cloudflare.com/client/v4', timeout_seconds: 30 },
    }
    await flush()

    current.value = {
      id: 'f2',
      name: 'Prod Cloudflare',
      provider: 'cloudflare_markdown',
      enable: true,
      config: { account_id: 'acc-1', api_token: 'cf-secret', base_url: 'https://cf.example/v4' },
    }
    await flush()

    expect(inputWithValue(root, 'Prod Cloudflare')).toBeTruthy()
    expect(inputWithValue(root, 'acc-1')).toBeTruthy()
    expect(inputWithValue(root, 'cf-secret')).toBeTruthy()
    expect(inputWithValue(root, 'https://cf.example/v4')).toBeTruthy()
  })

  it('does not mark untouched SearXNG headers dirty when editing base_url on a persisted provider', async () => {
    const { root, current } = await mountSetting(ProviderSetting, 'curSearchProvider', {
      id: 's1',
      name: 'SearXNG',
      provider: 'searxng',
      enable: true,
      config: { base_url: 'https://searx.example/search' },
    })

    await type(inputWithValue(root, 'https://searx.example/search')!, 'https://draft.searx.example/search')

    current.value = {
      id: 's1',
      name: 'SearXNG',
      provider: 'searxng',
      enable: true,
      config: { base_url: 'https://searx.example/search', headers: { Authorization: 'Bearer fresh-secret' } },
    }
    await flush()

    expect(inputWithValue(root, 'https://draft.searx.example/search')).toBeTruthy()
    expect(inputWithValue(root, 'Bearer fresh-secret')).toBeTruthy()
  })
})
