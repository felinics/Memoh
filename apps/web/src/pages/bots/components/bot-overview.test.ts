// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref } from 'vue'
import type { Slots } from 'vue'

const usage = vi.hoisted(() => ({ rows: [] as Array<Record<string, unknown>> }))

vi.mock('@pinia/colada', () => ({
  useQuery: ({ key }: { key: () => string[] }) => {
    const name = key()[0]
    const data = name === 'bot'
      ? { id: 'bot-1' }
      : name === 'bot-token-usage-overview' ? { chat: usage.rows } : undefined
    return { data: ref(data), isLoading: ref(false), refetch: vi.fn() }
  },
}))
vi.mock('@memohai/sdk', () => ({}))
vi.mock('vue-router', () => ({ useRoute: () => ({ params: { botName: 'bot-1' } }), useRouter: () => ({ push: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@vueuse/core', async importOriginal => ({ ...await importOriginal<typeof import('@vueuse/core')>(), useDark: () => ref(false) }))
vi.mock('vue-echarts', () => ({ default: () => h('div') }))
vi.mock('./bot-checks-panel.vue', () => ({ default: () => h('div') }))
vi.mock('@/components/channel-icon/index.vue', () => ({ default: () => h('div') }))
vi.mock('@felinic/ui', () => {
  const Passthrough = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', slots.default?.())
  return Object.fromEntries([
    'Badge', 'Button', 'CalloutBanner', 'MetricReadout', 'PageShell', 'SettingsRow', 'SettingsSection', 'Skeleton',
  ].map(name => [name, Passthrough]))
})

let app: ReturnType<typeof createApp> | undefined
let root: HTMLDivElement | undefined

afterEach(() => {
  app?.unmount()
  root?.remove()
  vi.useRealTimers()
})

async function cacheHitText(rows: Array<Record<string, unknown>>) {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-09-30T12:00:00'))
  usage.rows = rows
  const Overview = (await import('./bot-overview.vue')).default
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(Overview)
  app.config.globalProperties.$t = (key: string) => key
  app.mount(root)
  await nextTick()
  const label = [...root.querySelectorAll('p')].find(p => p.textContent === 'bots.overview.usageCacheHit')
  return label?.nextElementSibling?.textContent?.trim()
}

describe('bot overview cache hit rate', () => {
  it('shows reported cache use', async () => {
    expect(await cacheHitText([{ day: '2026-09-30', input_tokens: 1000, cache_read_tokens: 400, cache_read_tokens_reported: true }])).toBe('40.0%')
  })

  it('does not present unconfirmed cache use as a rate', async () => {
    expect(await cacheHitText([{ day: '2026-09-30', input_tokens: 10, cache_read_tokens: 200 }])).toBe('—')
    app?.unmount()
    root?.remove()
    expect(await cacheHitText([{ day: '2026-09-30', input_tokens: 1000, cache_read_tokens: 0, cache_read_tokens_reported: false }])).toBe('—')
  })
})

describe('bot overview daily rows', () => {
  it('adds same-day rows instead of keeping the last one', async () => {
    expect(await cacheHitText([
      { day: '2026-09-30', input_tokens: 1000, cache_read_tokens: 400, cache_read_tokens_reported: true },
      { day: '2026-09-30', input_tokens: 3000, cache_read_tokens: 0, cache_read_tokens_reported: true },
    ])).toBe('10.0%')
  })

  it('keeps an unreported same-day row from being dropped', async () => {
    expect(await cacheHitText([
      { day: '2026-09-30', input_tokens: 10, cache_read_tokens: 200 },
      { day: '2026-09-30', input_tokens: 1000, cache_read_tokens: 400, cache_read_tokens_reported: true },
    ])).toBe('—')
  })
})
