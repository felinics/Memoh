// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, reactive } from 'vue'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import PanelSupermarket from './panel-supermarket.vue'

const mocks = vi.hoisted(() => ({ push: vi.fn(), preview: vi.fn(), catalog: vi.fn(), scroll: vi.fn(), measure: vi.fn() }))
vi.mock('./use-sidebar-infinite-scroll', async importOriginal => {
  const original = await importOriginal<typeof import('./use-sidebar-infinite-scroll')>()
  return {
    ...original,
    useSidebarInfiniteScroll: (options: Parameters<typeof original.useSidebarInfiniteScroll>[0]) => {
      const result = original.useSidebarInfiniteScroll(options)
      mocks.scroll.mockImplementation(options.loadMore)
      mocks.measure.mockImplementation(result.measureScroll)
      return result
    },
  }
})
vi.mock('@/pages/supermarket/components/skill-icon.vue', () => ({ default: { template: '<span />' } }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: mocks.push }) }))
vi.mock('@memohai/sdk', async importOriginal => ({
  ...await importOriginal<object>(),
  getSupermarketApps: mocks.catalog,
  getSupermarketRegistriesByRegistryIdAppsByAppId: mocks.preview,
}))
vi.mock('@/pages/supermarket/components/install-app-dialog.vue', () => ({
  default: { props: ['open'], template: '<div v-if="open" data-install-dialog />' },
}))
vi.mock('@pinia/colada', async importOriginal => {
  const { ref } = await import('vue')
  return {
    ...await importOriginal<object>(),
    useQuery: (options: { key: () => string[] }) => ({
      data: ref(options.key()[0] === 'bot-apps'
        ? { workspace_state: 'running', items: [
          { registry_id: 'memoh', app_id: 'bun', name: 'Bun', author: { name: 'Memoh Team' }, status: 'partial' },
          { registry_id: 'memoh', app_id: 'node', name: 'Node.js', status: 'discovered', dependencies: [
            { id: 'other', dependency: { icon_url: '/workspace-dependencies/icons/' + 'b'.repeat(64) } },
            { id: 'node', dependency: { icon_url: '/workspace-dependencies/icons/' + 'a'.repeat(64) } },
          ] },
          { registry_id: 'memoh', app_id: 'uv', name: 'uv', status: 'discovered', dependencies: [
            { id: 'python-tools', dependency: { icon_url: '/workspace-dependencies/icons/' + 'c'.repeat(64) } },
          ] },
        ] }
        : { data: [{ registry_id: 'memoh', app_id: 'go', name: 'Go' }], total: 1 }),
      error: ref(null), isLoading: ref(false), refetch: vi.fn(),
    }),
  }
})
let app: ReturnType<typeof createApp>
let root: HTMLDivElement
let panelProps: { botId: string, botName?: string, canManage: boolean }
beforeEach(async () => {
  vi.clearAllMocks()
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  mocks.catalog.mockResolvedValue({ data: { data: [{ registry_id: 'memoh', app_id: 'go', name: 'Go' }], total: 90, limit: 30 } })
  mocks.preview.mockResolvedValue({ data: { registry_id: 'memoh', app_id: 'go' } })
  root = document.createElement('div')
  document.body.append(root)
  panelProps = reactive({ botId: 'qa-bot', canManage: true })
  app = createApp({ render: () => h(PanelSupermarket, panelProps) })
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
  app.mount(root)
  await nextTick()
})
afterEach(() => { app.unmount(); root.remove(); vi.unstubAllGlobals() })

function card(name: string) {
  const element = Array.from(root.querySelectorAll<HTMLElement>('[role="button"]')).find(el => el.textContent?.includes(name))
  expect(element).toBeDefined()
  return element!
}

it('opens the installed app management page from the whole card without a separate action', () => {
  expect(root.textContent).not.toContain('View details')
  card('Bun').click()
  expect(mocks.push).toHaveBeenCalledWith({ name: 'bot-detail', params: { botName: 'qa-bot' }, query: { tab: 'apps', app: 'memoh/bun' } })
})
it('prefers botName slug over botId when navigating from an installed app card', async () => {
  panelProps.botId = 'bot-uuid-1'
  panelProps.botName = 'neko'
  await nextTick()
  card('Bun').click()
  expect(mocks.push).toHaveBeenCalledWith({ name: 'bot-detail', params: { botName: 'neko' }, query: { tab: 'apps', app: 'memoh/bun' } })
  card('Go').click()
  expect(mocks.push).toHaveBeenCalledWith({ name: 'supermarket-app-detail', params: { registryId: 'memoh', appId: 'go' }, query: { botId: 'bot-uuid-1' } })
})
it('opens the market detail from the catalog card and preserves the selected bot', () => {
  card('Go').click()
  expect(mocks.push).toHaveBeenCalledWith({ name: 'supermarket-app-detail', params: { registryId: 'memoh', appId: 'go' }, query: { botId: 'qa-bot' } })
  expect(mocks.preview).not.toHaveBeenCalled()
})
it('keeps installation separate from card navigation, including keyboard events', async () => {
  const button = card('Go').querySelector('button')!
  button.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
  expect(mocks.push).not.toHaveBeenCalled()
  button.click()
  await nextTick()
  await nextTick()
  expect(mocks.preview).toHaveBeenCalledOnce()
  expect(root.querySelector('[data-install-dialog]')).not.toBeNull()
  expect(mocks.push).not.toHaveBeenCalled()
})
it.each(['Enter', ' '])('supports %s on the card itself', key => {
  card('Go').dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
  expect(mocks.push).toHaveBeenCalledOnce()
})

it('keeps publishers visible and places an installed warning in the action row', () => {
  const warning = Array.from(card('Bun').querySelectorAll('p')).find(el => el.textContent?.includes(en.apps.diagnostics.partial))
  expect(warning?.parentElement?.textContent).toContain('Memoh Team')
  expect(card('Node.js').textContent).toContain('memoh')
  expect(card('Go').querySelector('button')?.parentElement?.textContent).toContain('memoh')
})

it('uses dependency icons for discovered apps without showing a redundant source label', () => {
  expect(card('Node.js').querySelector('img')?.getAttribute('src')).toContain('/workspace-dependencies/icons/' + 'a'.repeat(64))
  expect(card('uv').querySelector('img')?.getAttribute('src')).toContain('/workspace-dependencies/icons/' + 'c'.repeat(64))
  expect(root.textContent).not.toContain(en.supermarket.sidebar.discovered)
})

/** The observer sees its marker before scrollHeight's trailing padding enters the same threshold. */
it('loads on the first marker intersection even when trailing padding is beyond the threshold', async () => {
  const viewport = root.querySelector<HTMLElement>('[data-slot="scroll-area-viewport"]')!
  Object.defineProperties(viewport, {
    clientHeight: { configurable: true, value: 600 },
    scrollHeight: { configurable: true, value: 1000 },
    scrollTop: { configurable: true, value: 185 },
  })
  vi.spyOn(viewport, 'getBoundingClientRect').mockReturnValue({ top: 0, bottom: 600 } as DOMRect)
  mocks.measure()
  await nextTick()
  const sentinel = root.querySelector<HTMLElement>('[aria-hidden="true"].h-px')!
  expect(sentinel).not.toBeNull()
  vi.spyOn(sentinel, 'getBoundingClientRect').mockReturnValue({ top: 790, bottom: 791 } as DOMRect)
  await mocks.scroll()
  expect(mocks.catalog).toHaveBeenCalledTimes(2)
  expect(mocks.catalog.mock.calls[1]![0].query.page).toBe(2)
})

it('does not prefetch an overflowing list while its marker is still far below the viewport', async () => {
  const viewport = root.querySelector<HTMLElement>('[data-slot="scroll-area-viewport"]')!
  Object.defineProperties(viewport, {
    clientHeight: { configurable: true, value: 600 },
    scrollHeight: { configurable: true, value: 1000 },
    scrollTop: { configurable: true, value: 0 },
  })
  vi.spyOn(viewport, 'getBoundingClientRect').mockReturnValue({ top: 0, bottom: 600 } as DOMRect)
  mocks.measure()
  await nextTick()
  const sentinel = root.querySelector<HTMLElement>('[aria-hidden="true"].h-px')!
  vi.spyOn(sentinel, 'getBoundingClientRect').mockReturnValue({ top: 975, bottom: 976 } as DOMRect)
  await mocks.scroll()
  expect(mocks.catalog).toHaveBeenCalledOnce()
})

it.each(['bot', 'permission'])('discards a pending installation preview after a %s change', async change => {
  let resolvePreview!: (value: unknown) => void
  mocks.preview.mockReturnValueOnce(new Promise(resolve => { resolvePreview = resolve }))
  card('Go').querySelector('button')!.click()
  await nextTick()
  expect(mocks.preview).toHaveBeenCalledOnce()
  if (change === 'bot') panelProps.botId = 'another-bot'
  else panelProps.canManage = false
  await nextTick()
  resolvePreview({ data: { registry_id: 'memoh', app_id: 'go' } })
  await nextTick()
  await nextTick()
  expect(root.querySelector('[data-install-dialog]')).toBeNull()
  expect(mocks.push).not.toHaveBeenCalled()
})
