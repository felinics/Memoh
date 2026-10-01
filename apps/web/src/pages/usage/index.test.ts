// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref } from 'vue'
import type { Slots } from 'vue'

const charts = vi.hoisted(() => [] as Array<{
  series: Array<{ name: string, data: number[] }>
  tooltip: { formatter: (params: unknown) => string }
}>)

vi.mock('@pinia/colada', () => ({
  useQuery: ({ key }: { key: () => string[] }) => ({
    data: ref(key()[0] === 'token-usage'
      ? {
          chat: [{ day: '2026-09-30', input_tokens: 350000, cache_read_tokens: 1, cache_read_tokens_reported: true }],
          by_model: [],
        }
      : { items: [] }),
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
    const UsagePage = (await import('./index.vue')).default
    root = document.createElement('div')
    document.body.append(root)
    app = createApp(UsagePage)
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    await nextTick()

    const chart = charts.find(option => option.series[0]?.name === 'usage.cacheHitRate')
    expect(chart).toBeDefined()
    const value = chart?.series[0]?.data[0]
    expect(value).toBeGreaterThan(0)
    expect(value).toBeCloseTo(1 / 350000 * 100, 10)
    expect(chart?.tooltip.formatter({ axisValueLabel: '2026-09-30', seriesName: 'usage.cacheHitRate', value })).toContain('<0.1%')
  })
})
