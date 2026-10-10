// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref } from 'vue'
import type { Slots } from 'vue'

const charts = vi.hoisted(() => [] as Array<{
  series: Array<{ name: string, data: number[] }>
  tooltip: { formatter: (params: unknown) => string }
}>)

const usage = vi.hoisted(() => ({ data: {} as Record<string, unknown> }))
const tinyRateUsage = {
  chat: [{ day: '2026-09-30', input_tokens: 350000, cache_read_tokens: 1, cache_read_tokens_reported: true }],
  by_model: [],
}

vi.mock('@pinia/colada', () => ({
  useQuery: ({ key }: { key: () => string[] }) => ({
    data: ref(key()[0] === 'token-usage' ? usage.data : { items: [] }),
    asyncStatus: ref('idle'),
    refetch: vi.fn(),
  }),
}))
vi.mock('@memohai/sdk/colada', () => ({ getBotsQuery: () => ({ key: () => ['bots'] }) }))
vi.mock('@memohai/sdk', () => ({ getBotsByBotIdTokenUsage: vi.fn(), getBotsByBotIdTokenUsageRecords: vi.fn() }))
vi.mock('@/store/chat-selection', () => ({ useChatSelectionStore: () => ({ currentBotId: '' }) }))
vi.mock('@/composables/useSyncedQueryParam', () => ({
  useSyncedQueryParam: (key: string, fallback: string) => ref(({ bot: 'fixture', from: '2026-09-30', to: '2026-10-01' } as Record<string, string>)[key] ?? fallback),
}))
vi.mock('@vueuse/core', () => ({ useDark: () => ref(false) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: ref('en') }) }))
vi.mock('@/components/bot-select/index.vue', () => ({ default: () => h('div') }))
vi.mock('@felinic/ui', () => {
  const Passthrough = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', slots.default?.())
  return Object.fromEntries([
    'DateRangePicker', 'Empty', 'EmptyHeader', 'EmptyTitle', 'Pagination', 'PaginationContent',
    'PaginationEllipsis', 'PaginationFirst', 'PaginationItem', 'PaginationLast', 'PaginationNext',
    'PaginationPrevious', 'Select', 'SelectContent', 'SelectItem', 'SelectTrigger', 'SelectValue',
    'Spinner', 'Table', 'TableBody', 'TableCell', 'TableHead', 'TableHeader', 'TableRow',
    'MetricReadout', 'PageShell', 'SectionGroup', 'SettingsSection',
  ].map(name => [name, Passthrough]))
})
vi.mock('vue-echarts', () => ({
  default: {
    props: ['option'],
    setup(props: { option: typeof charts[number] }) {
      return () => {
        charts.push(props.option)
        return h('div')
      }
    },
  },
}))

let app: ReturnType<typeof createApp> | undefined
let root: HTMLDivElement | undefined

async function mountUsagePage(data: Record<string, unknown>) {
  usage.data = data
  const UsagePage = (await import('./index.vue')).default
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(UsagePage)
  app.config.globalProperties.$t = (key: string) => key
  app.mount(root)
  await nextTick()
  return root
}

afterEach(() => {
  app?.unmount()
  root?.remove()
  charts.length = 0
  vi.useRealTimers()
})

describe('cache hit rate chart', () => {
  it('passes a positive rate to the chart and displays it below 0.1 percent', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-09-30T12:00:00Z'))
    await mountUsagePage(tinyRateUsage)

    const chart = charts.find(option => option.series[0]?.name === 'usage.cacheHitRate')
    expect(chart).toBeDefined()
    const value = chart?.series[0]?.data[0]
    expect(value).toBeGreaterThan(0)
    expect(value).toBeCloseTo(1 / 350000 * 100, 10)
    expect(chart?.tooltip.formatter({ axisValueLabel: '2026-09-30', seriesName: 'usage.cacheHitRate', value })).toContain('<0.1%')
  })
})

describe('cache usage hint', () => {
  it.each([
    ['no usage in range', { by_model: [] }, false],
    ['only reported rows', tinyRateUsage, false],
    ['an unreported row', { chat: [{ day: '2026-09-30', input_tokens: 10, cache_read_tokens: 200 }], by_model: [] }, true],
  ])('with %s', async (_name, data, shown) => {
    const page = await mountUsagePage(data)
    expect(page.textContent?.includes('usage.cacheUsageUnavailable')).toBe(shown)
  })
})

describe('rows for the same day', () => {
  const day = '2026-09-30'
  const reported = [
    { day, input_tokens: 1000, output_tokens: 10, cache_read_tokens: 400, cache_read_tokens_reported: true },
    { day, input_tokens: 3000, output_tokens: 20, cache_read_tokens: 0, cache_read_tokens_reported: true },
  ]
  const mixed = [
    { day, input_tokens: 10, cache_read_tokens: 200 },
    { day, input_tokens: 100, cache_read_tokens: 0, cache_read_tokens_reported: true },
  ]
  const seriesValue = (name: string) => charts.find(option => option.series.some(series => series.name === name))
    ?.series.find(series => series.name === name)?.data[0]

  it.each([['in order', reported], ['reversed', [...reported].reverse()]])('add up when %s', async (_name, chat) => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-09-30T12:00:00Z'))
    await mountUsagePage({ chat, by_model: [] })

    expect(seriesValue('usage.cacheRead')).toBe(400)
    expect(seriesValue('usage.noCache')).toBe(3600)
    expect(seriesValue('usage.cacheHitRate')).toBeCloseTo(10, 10)
  })

  it.each([['in order', mixed], ['reversed', [...mixed].reverse()]])('keep an unreported row visible when %s', async (_name, chat) => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-09-30T12:00:00Z'))
    const page = await mountUsagePage({ chat, by_model: [] })

    expect(page.textContent?.includes('usage.cacheUsageUnavailable')).toBe(true)
  })
})
