// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, type App } from 'vue'
import { createPinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import UsagePage from './index.vue'

const api = vi.hoisted(() => ({
  bots: vi.fn(),
  usage: vi.fn(),
  records: vi.fn(),
  route: { query: { bot: 'bot-1' } as Record<string, string> },
  currentBotId: 'bot-1',
}))
vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdTokenUsage: api.usage,
  getBotsByBotIdTokenUsageRecords: api.records,
}))
vi.mock('@memohai/sdk/colada', () => ({
  getBotsQuery: () => ({ key: ['bots'], query: () => api.bots() }),
}))
vi.mock('vue-i18n', async () => {
  const { ref } = await import('vue')
  return { useI18n: () => ({ t: (key: string) => key, locale: ref('en') }) }
})
vi.mock('vue-router', () => ({
  useRoute: () => api.route,
  useRouter: () => ({ replace: vi.fn() }),
}))
vi.mock('vue-echarts', () => ({ default: () => h('div', { 'data-testid': 'chart' }) }))
vi.mock('echarts/core', () => ({ use: vi.fn() }))
vi.mock('echarts/renderers', () => ({ CanvasRenderer: {} }))
vi.mock('echarts/charts', () => ({ LineChart: {}, BarChart: {}, PieChart: {} }))
vi.mock('echarts/components', () => ({ GridComponent: {}, TooltipComponent: {}, LegendComponent: {} }))
vi.mock('@/components/bot-select/index.vue', () => ({ default: () => h('div') }))
vi.mock('@/store/chat-selection', () => ({ useChatSelectionStore: () => ({ get currentBotId() { return api.currentBotId } }) }))
vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })

let app: App | null = null
afterEach(() => {
  app?.unmount()
  app = null
  document.body.innerHTML = ''
  api.route.query = { bot: 'bot-1' }
  api.currentBotId = 'bot-1'
  vi.clearAllMocks()
})

async function mountPage(): Promise<HTMLElement> {
  api.bots.mockResolvedValue({ items: [{ id: 'bot-1', display_name: 'Bot' }] })
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(UsagePage)
  app.use(createPinia()).use(PiniaColada)
  app.config.globalProperties.$t = (key: string) => key
  app.mount(root)
  await vi.waitFor(() => expect(api.usage).toHaveBeenCalled())
  await vi.waitFor(() => expect(api.records).toHaveBeenCalled())
  await new Promise(resolve => setTimeout(resolve, 0))
  await nextTick()
  return root
}

describe('usage page load failures', () => {
  it('shows a load failure instead of an all-zero overview when the summary request fails', async () => {
    api.usage.mockRejectedValue({ status: 500 })
    api.records.mockRejectedValue({ status: 500 })
    const root = await mountPage()
    expect(root.textContent).toContain('usage.loadFailed')
    expect(root.textContent).not.toContain('usage.overview')
    expect(root.textContent).not.toContain('usage.noData')
  })

  it('shows a load failure instead of "no records" when only the records request fails', async () => {
    api.usage.mockResolvedValue({ data: { by_model: [], by_day: [] } })
    api.records.mockRejectedValue({ status: 500 })
    const root = await mountPage()
    expect(root.textContent).toContain('usage.overview')
    expect(root.textContent).not.toContain('usage.noRecords')
    expect(root.textContent).toContain('usage.loadFailed')
  })

  it('retries both requests from the failure banner without flashing a full-page spinner mid-retry', async () => {
    api.usage.mockRejectedValue({ status: 500 })
    api.records.mockRejectedValue({ status: 500 })
    const root = await mountPage()
    let resolveUsage!: (val: unknown) => void
    api.usage.mockImplementation(() => new Promise((resolve) => { resolveUsage = resolve }))
    api.records.mockResolvedValue({ data: { items: [], total: 0 } })
    const calls = api.usage.mock.calls.length
    const retry = [...root.querySelectorAll('button')].find(b => b.textContent?.includes('common.retry'))
    expect(retry).toBeTruthy()
    retry!.click()
    await vi.waitFor(() => expect(api.usage.mock.calls.length).toBeGreaterThan(calls))
    await nextTick()
    expect(root.textContent).toContain('usage.loadFailed')

    resolveUsage({ data: { by_model: [], by_day: [] } })
    await new Promise(resolve => setTimeout(resolve, 0))
    await nextTick()
    expect(root.textContent).toContain('usage.overview')
    expect(root.textContent).not.toContain('usage.loadFailed')
  })

  it('shows a load failure with retry instead of the select-bot empty state when loading bots fails without a bot param', async () => {
    api.route.query = {}
    api.currentBotId = ''
    api.bots.mockRejectedValueOnce({ status: 500 })
    api.usage.mockResolvedValue({ data: { by_model: [], by_day: [] } })
    api.records.mockResolvedValue({ data: { items: [], total: 0 } })

    const root = document.createElement('div')
    document.body.append(root)
    app = createApp(UsagePage)
    app.use(createPinia()).use(PiniaColada)
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    await vi.waitFor(() => expect(api.bots).toHaveBeenCalledOnce())
    await new Promise(resolve => setTimeout(resolve, 0))
    await nextTick()

    expect(root.textContent).toContain('usage.loadFailed')
    expect(root.textContent).not.toContain('usage.selectBotPlaceholder')

    api.bots.mockResolvedValue({ items: [{ id: 'bot-1', display_name: 'Bot' }] })
    const retry = [...root.querySelectorAll('button')].find(b => b.textContent?.includes('common.retry'))
    expect(retry).toBeTruthy()
    retry!.click()
    await vi.waitFor(() => expect(api.usage).toHaveBeenCalled())
    await new Promise(resolve => setTimeout(resolve, 0))
    await nextTick()

    expect(root.textContent).toContain('usage.overview')
    expect(root.textContent).not.toContain('usage.loadFailed')
  })
})
