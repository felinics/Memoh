// @vitest-environment jsdom
/**
 * Failure modes covered for session logout, 401 unauthorized clearing, and route guards:
 * 1. `exitLogin()` clears `token` in localStorage, yet leaves stale entries inside
 *    in-memory `@pinia/colada` `queryCache`, leaves user-scoped localStorage keys
 *    (`memoh:query-cache`, `bot-order`, `workspace-working-workdir`), or lets the
 *    1s debounced query-cache saver resurrect `memoh:query-cache` after 2s.
 * 2. `exitLogin()` or `notifyAuthSessionCleared('unauthorized')` triggers while the
 *    user is on `/connect` (Desktop server switcher) or `/oauth/mcp-callback`
 *    (OAuth popup callback), hijacking the route to `/login` and breaking server
 *    switching or OAuth token exchange.
 * 3. `exitLogin()` or `401 unauthorized` on an authenticated page (`/bot/kitty-3`,
 *    `/settings/profile`) fails to reset `userInfo` or fails to redirect to `/login`.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, type App, type Component } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { PiniaColada, useQueryCache } from '@pinia/colada'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import {
  QUERY_CACHE_STORAGE_KEY,
  cancelPendingQueryCacheSave,
  createQueryCachePersistencePlugin,
  saveQueryCacheToDiskNow,
} from '@/lib/query-cache-persistence'
import { notifyAuthSessionCleared } from '@/lib/auth-session'
import { useUserStore } from './user'

const routerState = vi.hoisted(() => ({
  active: undefined as Router | undefined,
}))

vi.mock('vue-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-router')>()
  return {
    ...actual,
    useRouter: () => routerState.active,
  }
})

vi.mock('@memohai/sdk', () => ({
  getUsersMe: vi.fn().mockResolvedValue({ data: null }),
  putUsersMe: vi.fn().mockResolvedValue({ data: {} }),
}))

const EmptyPage: Component = { render: () => h('div') }

async function settleRouter() {
  for (let i = 0; i < 8; i++) {
    await Promise.resolve()
  }
}

function installMemoryLocalStorage() {
  const map = new Map<string, string>()
  const storage: Storage = {
    get length() { return map.size },
    clear() { map.clear() },
    getItem(key: string) { return map.get(key) ?? null },
    key(index: number) { return Array.from(map.keys())[index] ?? null },
    removeItem(key: string) { map.delete(key) },
    setItem(key: string, value: string) { map.set(key, String(value)) },
  }
  Object.defineProperty(globalThis, 'localStorage', { value: storage, configurable: true, writable: true })
  Object.defineProperty(window, 'localStorage', { value: storage, configurable: true, writable: true })
  return storage
}

async function mountSessionHarness(initialPath: string) {
  const storage = installMemoryLocalStorage()
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', name: 'Home', component: EmptyPage },
      { path: '/login', name: 'Login', component: EmptyPage },
      { path: '/connect', name: 'DesktopConnect', component: EmptyPage },
      { path: '/oauth/mcp-callback', name: 'McpOAuthCallback', component: EmptyPage },
      { path: '/bot/:botId', name: 'BotChat', component: EmptyPage },
      { path: '/settings/profile', name: 'Profile', component: EmptyPage },
    ],
  })
  await router.push(initialPath)
  await router.isReady()
  routerState.active = router
  storage.setItem('token', 'header.eyJ1c2VyX2lkIjoidXNlci0xIn0.sig')

  const pinia = createPinia()
  setActivePinia(pinia)

  let userStore!: ReturnType<typeof useUserStore>
  let queryCache!: ReturnType<typeof useQueryCache>

  const Host: Component = {
    setup() {
      queryCache = useQueryCache()
      userStore = useUserStore()
      return () => h('div')
    },
  }

  const app = createApp(Host)
  app.use(pinia)
  app.use(PiniaColada, {
    plugins: [createQueryCachePersistencePlugin({ storage })],
  })
  app.use(router)
  const container = document.createElement('div')
  app.mount(container)

  // Seed an active authenticated session + cached queries + user-scoped localStorage keys.
  userStore.login(
    {
      id: 'user-1',
      username: 'alice',
      role: 'admin',
      displayName: 'Alice',
      avatarUrl: '',
      timezone: 'Asia/Shanghai',
    },
    'header.eyJ1c2VyX2lkIjoidXNlci0xIn0.sig',
  )
  await router.push(initialPath)

  const entry = queryCache.ensure({ key: ['bots-list'], query: async () => [{ id: 'kitty-3' }] })
  queryCache.setEntryState(entry, {
    status: 'success',
    data: [{ id: 'kitty-3' }],
    error: null,
  })
  saveQueryCacheToDiskNow(queryCache, storage)
  storage.setItem('pinned-bot-ids', '["kitty-3"]')
  storage.setItem('workspace-layout', '{"panels":["kitty-3"]}')

  return { app, container, router, storage, userStore, queryCache }
}

describe('useUserStore logout & unauthorized cleanup across routes', () => {
  let activeApp: App | undefined

  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    activeApp?.unmount()
    activeApp = undefined
    routerState.active = undefined
    cancelPendingQueryCacheSave()
    vi.useRealTimers()
  })

  it('clears token, userInfo, in-memory queryCache, and disk cache without resurrection after 2s, and redirects normal routes to /login', async () => {
    const harness = await mountSessionHarness('/bot/kitty-3')
    activeApp = harness.app
    const replaceSpy = vi.spyOn(harness.router, 'replace')

    expect(harness.queryCache.getEntries().length).toBeGreaterThan(0)
    expect(harness.storage.getItem(QUERY_CACHE_STORAGE_KEY)).toContain('kitty-3')

    harness.userStore.exitLogin()
    await Promise.all(replaceSpy.mock.results.map(r => r.value))

    expect(harness.userStore.userInfo.id).toBe('')
    expect(harness.userStore.userInfo.username).toBe('')
    expect(harness.storage.getItem('token') ?? '').toBe('')
    expect(harness.queryCache.getEntries()).toHaveLength(0)
    expect(harness.storage.getItem(QUERY_CACHE_STORAGE_KEY)).toBeNull()
    expect(harness.storage.getItem('pinned-bot-ids')).toBeNull()
    expect(harness.storage.getItem('workspace-layout')).toBeNull()

    // Advance past the 1000ms debounce window to ensure `remove()` did not re-save stale entries.
    vi.advanceTimersByTime(2000)
    await settleRouter()
    expect(harness.storage.getItem(QUERY_CACHE_STORAGE_KEY)).toBeNull()
    expect(harness.router.currentRoute.value.path).toBe('/login')
  })

  it.each([
    '/connect',
    '/oauth/mcp-callback?code=c1&state=s1',
  ])('clears session and caches on exitLogin() and 401 unauthorized WITHOUT hijacking %s to /login', async (specialRoute) => {
    const harness = await mountSessionHarness(specialRoute)
    activeApp = harness.app
    const expectedPath = specialRoute.split('?')[0]
    const replaceSpy = vi.spyOn(harness.router, 'replace')

    harness.userStore.exitLogin()
    await settleRouter()
    vi.advanceTimersByTime(2000)

    expect(replaceSpy).not.toHaveBeenCalled()
    expect(harness.userStore.userInfo.id).toBe('')
    expect(harness.queryCache.getEntries()).toHaveLength(0)
    expect(harness.storage.getItem(QUERY_CACHE_STORAGE_KEY)).toBeNull()
    expect(harness.router.currentRoute.value.path).toBe(expectedPath)

    // Re-seed and trigger a 401 unauthorized broadcast on the same protected route.
    harness.userStore.login(
      { id: 'user-2', username: 'bob', role: 'member', displayName: 'Bob', avatarUrl: '', timezone: 'UTC' },
      'header.eyJ1c2VyX2lkIjoidXNlci0yIn0.sig',
    )
    await harness.router.push(specialRoute)
    replaceSpy.mockClear()
    const entry = harness.queryCache.ensure({ key: ['models'], query: async () => ['gpt-5'] })
    harness.queryCache.setEntryState(entry, { status: 'success', data: ['gpt-5'], error: null })
    saveQueryCacheToDiskNow(harness.queryCache, harness.storage)

    notifyAuthSessionCleared('unauthorized')
    await settleRouter()
    vi.advanceTimersByTime(2000)

    expect(replaceSpy).not.toHaveBeenCalled()
    expect(harness.userStore.userInfo.id).toBe('')
    expect(harness.queryCache.getEntries()).toHaveLength(0)
    expect(harness.storage.getItem(QUERY_CACHE_STORAGE_KEY)).toBeNull()
    expect(harness.router.currentRoute.value.path).toBe(expectedPath)
  })

  it('clears queryCache and redirects to /login when 401 unauthorized fires on /settings/profile', async () => {
    const harness = await mountSessionHarness('/settings/profile')
    activeApp = harness.app
    const replaceSpy = vi.spyOn(harness.router, 'replace')

    notifyAuthSessionCleared('unauthorized')
    await Promise.all(replaceSpy.mock.results.map(r => r.value))
    vi.advanceTimersByTime(2000)

    expect(harness.userStore.userInfo.id).toBe('')
    expect(harness.queryCache.getEntries()).toHaveLength(0)
    expect(harness.storage.getItem(QUERY_CACHE_STORAGE_KEY)).toBeNull()
    expect(harness.router.currentRoute.value.path).toBe('/login')
  })
})
