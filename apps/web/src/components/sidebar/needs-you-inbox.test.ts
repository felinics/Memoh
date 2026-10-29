// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, reactive } from 'vue'
import NeedsYouInbox from './needs-you-inbox.vue'

const push = vi.fn()
const fresh = new Set<string>()
const unread = reactive({
  total: 0,
  inbox: [] as Array<{ sessionId: string, botId: string, title: string, kind: string, attentionAt: string }>,
  isFresh: (sessionId: string, attentionAt: string) => fresh.has(`${sessionId}@${attentionAt}`),
})
const motion = vi.hoisted(() => ({
  enterRow: vi.fn((_el: Element, done: () => void) => done()),
  revealRow: vi.fn((_el: Element, done: () => void) => done()),
  leaveRow: vi.fn((_el: Element, done: () => void) => done()),
  flashRow: vi.fn(),
  popDot: vi.fn(),
  bounce: vi.fn(),
}))
vi.mock('@/utils/attention-motion', () => motion)

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string, args?: { count?: number }) => args?.count ? `${key}(${args.count})` : key, locale: { value: 'en' } }),
}))
vi.mock('@felinic/ui', () => {
  const tag = (name: string) => defineComponent({ inheritAttrs: false, setup: (_, { slots, attrs }) => () => h(name, attrs, slots.default?.()) })
  return {
    Avatar: tag('span'),
    AvatarImage: tag('img'),
    AvatarFallback: tag('span'),
    BadgeCount: defineComponent({ props: { count: Number }, setup: props => () => h('span', String(props.count)) }),
    TextButton: tag('button'),
  }
})
vi.mock('vue-router', () => ({ useRouter: () => ({ push }) }))
vi.mock('@/store/unread', () => ({ useUnreadStore: () => unread }))
vi.mock('@/store/chat-list', () => ({ useChatStore: () => ({ bots: [{ id: 'b1', name: 'kitty', display_name: 'Kitty' }] }) }))

function entries(count: number) {
  return Array.from({ length: count }, (_, i) => ({
    sessionId: `s${i}`,
    botId: 'b1',
    title: `Session ${i}`,
    kind: i === 0 ? 'needs_input' : 'reply',
    attentionAt: new Date(Date.now() - i * 60_000).toISOString(),
  }))
}

function mount() {
  const root = document.createElement('div')
  const app = createApp(NeedsYouInbox)
  app.mount(root)
  return { root, app }
}

afterEach(() => {
  unread.total = 0
  unread.inbox = []
  fresh.clear()
  push.mockClear()
  Object.values(motion).forEach(fn => fn.mockClear())
})

describe('needs-you inbox', () => {
  it('renders nothing while there is no unread session', () => {
    const { root, app } = mount()
    expect(root.querySelector('[data-slot="needs-you-card"]')).toBeNull()
    app.unmount()
  })

  it('caps the list at five rows and expands in place', async () => {
    unread.inbox = entries(6)
    unread.total = 6
    const { root, app } = mount()
    expect(root.querySelectorAll('[data-slot="needs-you-row"]')).toHaveLength(5)
    const more = [...root.querySelectorAll('button')].find(button => button.textContent?.includes('notifications.showAll(6)'))
    expect(more).toBeTruthy()
    more!.click()
    await nextTick()
    expect(root.querySelectorAll('[data-slot="needs-you-row"]')).toHaveLength(6)
    app.unmount()
  })

  it('labels the kind and opens the session across bots on click', () => {
    unread.inbox = entries(2)
    unread.total = 2
    const { root, app } = mount()
    const first = root.querySelector<HTMLButtonElement>('[data-slot="needs-you-row"]')!
    expect(first.getAttribute('aria-label')).toBe('Session 0, Kitty, notifications.kind.needs_input')
    expect(first.querySelector('[data-slot="attention-dot"]')?.getAttribute('data-kind')).toBe('needs_input')
    first.click()
    expect(push).toHaveBeenCalledWith('/bot/b1?session=s0')
    app.unmount()
  })

  it('does not replay the arrival highlight when expanding older rows', async () => {
    unread.inbox = entries(7)
    unread.total = 7
    const { root, app } = mount()
    const more = [...root.querySelectorAll('button')].find(button => button.textContent?.includes('notifications.showAll(7)'))!
    more.click()
    await nextTick()
    expect(root.querySelectorAll('[data-slot="needs-you-row"]')).toHaveLength(7)
    expect(motion.revealRow).toHaveBeenCalledTimes(2)
    expect(motion.enterRow).not.toHaveBeenCalled()
    expect(motion.flashRow).not.toHaveBeenCalled()
    expect(motion.popDot).not.toHaveBeenCalled()
    app.unmount()
  })

  it('highlights a row only when its attention just arrived', async () => {
    unread.inbox = entries(2)
    unread.total = 2
    const { app } = mount()
    expect(motion.flashRow).not.toHaveBeenCalled()

    const arrived = { sessionId: 'new', botId: 'b1', title: 'New', kind: 'reply', attentionAt: new Date().toISOString() }
    fresh.add(`new@${arrived.attentionAt}`)
    unread.inbox = [arrived, ...unread.inbox]
    unread.total = 3
    await nextTick()
    await nextTick()

    expect(motion.flashRow).toHaveBeenCalledTimes(1)
    const flashed = motion.flashRow.mock.calls[0]![0] as HTMLElement
    expect(flashed.closest('[data-slot="needs-you-row"]')?.getAttribute('aria-label')).toContain('New')
    expect(motion.popDot).toHaveBeenCalledTimes(1)
    app.unmount()
  })
})
