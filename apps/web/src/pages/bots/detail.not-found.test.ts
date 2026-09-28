// @vitest-environment jsdom
import { createApp, defineComponent, h, nextTick, reactive, type App, type Slots } from 'vue'
import { createPinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import BotDetail from './detail.vue'

const sdk = vi.hoisted(() => ({ getBot: vi.fn() }))
const route = vi.hoisted(() => ({ value: null as null | { name: string, params: Record<string, string>, query: Record<string, string> } }))

vi.mock('@memohai/sdk', () => ({
  getBotsById: sdk.getBot,
  putBotsById: vi.fn(),
  getBotsByIdChecks: vi.fn(async () => ({ data: { items: [] } })),
  getBotsByBotIdContainer: vi.fn(async () => ({ data: undefined })),
  getBotsByBotIdContainerSnapshots: vi.fn(async () => ({ data: { snapshots: [] } })),
}))
vi.mock('@memohai/sdk/colada', () => ({ getBotsQueryKey: () => ['bots'] }))
vi.mock('vue-router', () => ({
  useRoute: () => route.value,
  useRouter: () => ({ replace: vi.fn(async () => {}), push: vi.fn(async () => {}), back: vi.fn() }),
  onBeforeRouteLeave: vi.fn(),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@felinic/ui', async () => {
  const { h } = await import('vue')
  const pass = (tag: string) => (_: unknown, { slots }: { slots: Slots }) => h(tag, slots.default?.())
  return {
    Avatar: pass('div'), AvatarImage: pass('div'), AvatarFallback: pass('div'),
    Input: pass('div'), SidebarMenu: pass('div'), SidebarMenuItem: pass('div'),
    SettingsSection: pass('section'), BadgeCount: pass('span'), NavItem: pass('div'),
    Button: (_: unknown, { slots, attrs }: { slots: Slots, attrs: Record<string, unknown> }) =>
      h('button', { onClick: attrs.onClick }, slots.default?.()),
    PanePlaceholder: (props: { title?: string }, { slots }: { slots: Slots }) =>
      h('div', { 'data-pane': '' }, [props.title, slots.default?.(), slots.action?.()]),
    SettingsRow: (props: { label?: string, description?: string }, { slots }: { slots: Slots }) =>
      h('div', { 'data-row': '' }, [props.label, props.description, slots.default?.()]),
    toast: { success: vi.fn(), error: vi.fn() },
  }
})
vi.mock('@memohai/icon/ui', () => ({ SettingsIcon: 'svg' }))
vi.mock('@/store/capabilities', () => ({ useCapabilitiesStore: () => ({ load: vi.fn(), loaded: true }) }))
vi.mock('@/composables/api/useApps', async () => {
  const { ref } = await import('vue')
  return { appNeedsAttention: () => false, useBotAppsQuery: () => ({ data: ref(undefined) }) }
})
vi.mock('@/composables/useBackOr', () => ({ useBackAffordance: () => ({ onBack: vi.fn(), label: 'bots' }) }))
vi.mock('@/composables/useSyncedQueryParam', async () => {
  const { ref } = await import('vue')
  return { useSyncedQueryParam: () => ref('overview') }
})
vi.mock('@/composables/useIsMobile', async () => {
  const { ref } = await import('vue')
  return { useIsMobile: () => ref(false) }
})
vi.mock('@/composables/useMacTrafficReserve', async () => {
  const { computed } = await import('vue')
  return { useMacTrafficReserve: () => computed(() => false) }
})
vi.mock('@/components/master-detail-sidebar-layout/index.vue', async () => {
  const { h } = await import('vue')
  return {
    default: (_: unknown, { slots }: { slots: Slots }) =>
      h('div', [slots['sidebar-header']?.(), slots['sidebar-content']?.(), slots.detail?.()]),
  }
})
vi.mock('./components/bot-advanced.vue', () => ({ default: () => null }))
vi.mock('./components/bot-settings.vue', () => ({ default: () => null }))
vi.mock('./components/bot-channels.vue', () => ({ default: () => null }))
vi.mock('./components/bot-mcp.vue', () => ({ default: () => null }))
vi.mock('./components/bot-memory.vue', () => ({ default: () => null }))
vi.mock('./components/bot-container.vue', () => ({ default: () => null }))
vi.mock('./components/bot-remote-runtime.vue', () => ({ default: () => null }))
vi.mock('./components/bot-access.vue', () => ({ default: () => null }))
vi.mock('./components/bot-agents.vue', () => ({ default: () => null }))
vi.mock('./components/bot-apps.vue', () => ({ default: () => null }))
vi.mock('./components/avatar-edit-dialog.vue', () => ({ default: () => null }))
vi.mock('./components/bot-overview.vue', async () => {
  const { h } = await import('vue')
  return { default: () => h('div', 'overview-tab') }
})

const existingBot = { id: 'bot-1', name: 'errs-bot', display_name: 'Errs Bot', status: 'ready' }

let app: App | undefined
let host: HTMLDivElement | undefined

async function flush() {
  for (let i = 0; i < 6; i += 1) {
    await new Promise(resolve => setTimeout(resolve, 0))
    await nextTick()
  }
}

async function mountDetail(botName: string) {
  route.value = reactive({ name: 'bot-detail', params: { botName }, query: {} })
  host = document.createElement('div')
  document.body.append(host)
  app = createApp(defineComponent({ setup: () => () => h(BotDetail) }))
  app.config.globalProperties.$t = (key: string) => key
  app.use(createPinia()).use(PiniaColada)
  app.mount(host)
  await flush()
  return host
}

beforeEach(() => {
  sdk.getBot.mockReset()
})
afterEach(() => {
  app?.unmount()
  host?.remove()
  app = undefined
  host = undefined
})

describe('bot detail page', () => {
  it('shows the not-found state when the bot does not exist', async () => {
    // Legacy 404: the body carries only a message, the status is on the response.
    sdk.getBot.mockResolvedValue({ data: undefined, error: { message: 'bot not found' }, response: { status: 404 } })

    const el = await mountDetail('no-such-bot')

    expect(el.textContent).toContain('bots.notFound')
    expect(el.textContent).toContain('bots.notFoundDescription')
    expect(el.textContent).not.toContain('overview-tab')
    expect(el.textContent).not.toContain('common.retry')
  })

  it('shows a retryable load error for other failures, then the bot once it loads', async () => {
    sdk.getBot.mockResolvedValueOnce({ data: undefined, error: { message: 'upstream down' }, response: { status: 503 } })

    const el = await mountDetail('errs-bot')

    expect(el.textContent).toContain('bots.loadFailed')
    expect(el.textContent).toContain('upstream down')
    expect(el.textContent).not.toContain('bots.notFound')

    sdk.getBot.mockResolvedValueOnce({ data: existingBot, error: undefined, response: { status: 200 } })
    el.querySelector<HTMLButtonElement>('[data-row] button')!.click()
    await flush()

    expect(el.textContent).toContain('overview-tab')
    expect(el.textContent).not.toContain('bots.loadFailed')
  })

  it('renders the tabs for an existing bot', async () => {
    sdk.getBot.mockResolvedValue({ data: existingBot, error: undefined, response: { status: 200 } })

    const el = await mountDetail('errs-bot')

    expect(el.textContent).toContain('overview-tab')
    expect(el.textContent).not.toContain('bots.notFound')
  })
})
