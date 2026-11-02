// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, reactive, type EffectScope } from 'vue'
import { useSessionNotifications } from './useSessionNotifications'
import type { UnreadEntry } from '@/store/unread'

const deps = vi.hoisted(() => ({ viewed: vi.fn(), push: vi.fn(), listener: null as null | ((entry: UnreadEntry) => void) }))
const unread = reactive({
  total: 0,
  entries: {} as Record<string, UnreadEntry>,
  onAttention: (listener: (entry: UnreadEntry) => void) => {
    deps.listener = listener
    return () => { deps.listener = null }
  },
})
const settings = reactive({ active: true, desktop: false })
vi.mock('@/store/unread', () => ({ useUnreadStore: () => unread }))
vi.mock('@/store/chat-list', () => ({ useChatStore: () => ({ bots: [{ id: 'b1', display_name: 'Atlas' }] }) }))
vi.mock('@/store/notification-settings', () => ({ useNotificationSettings: () => settings }))
vi.mock('./useSessionViewing', () => ({ isSessionViewed: deps.viewed }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: deps.push }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const entry: UnreadEntry = { sessionId: 's1', botId: 'b1', attentionAt: '2026-10-20T08:00:00Z', readThrough: '2026-10-20T08:00:00Z', marked: false, title: 'Review', kind: 'reply' }
let scope: EffectScope
const sent: Array<{ title: string, options: NotificationOptions, onclick: null | (() => void) }> = []
const flush = async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve() }

beforeEach(() => {
  const storage = new Map<string, string>()
  vi.stubGlobal('localStorage', {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => storage.set(key, value),
  })
  sent.length = 0
  unread.entries = {}
  unread.total = 0
  settings.active = true
  settings.desktop = false
  deps.viewed.mockReset().mockReturnValue(false)
  deps.push.mockReset()
  vi.stubGlobal('BroadcastChannel', undefined)
  vi.stubGlobal('Notification', class {
    onclick = null
    title: string
    options: NotificationOptions
    constructor(title: string, options: NotificationOptions) {
      this.title = title
      this.options = options
      sent.push(this)
    }
    close() {}
  })
  scope = effectScope()
  scope.run(useSessionNotifications)
})
afterEach(() => { scope.stop(); vi.unstubAllGlobals(); vi.restoreAllMocks(); delete window.api })

async function emit(value = entry) {
  unread.entries[value.sessionId] = value
  deps.listener?.(value)
  await flush()
}

describe('system notification delivery', () => {
  it('does not notify from a snapshot, a viewed session, or a disabled setting', async () => {
    unread.entries.s1 = entry
    await flush()
    expect(sent).toHaveLength(0)
    deps.viewed.mockReturnValue(true)
    await emit()
    deps.viewed.mockReturnValue(false)
    settings.active = false
    await emit()
    expect(sent).toHaveLength(0)
  })

  it('deduplicates, uses fixed text, and opens the destination on click', async () => {
    vi.spyOn(window, 'focus').mockImplementation(() => {})
    await emit()
    await emit()
    expect(sent).toHaveLength(1)
    expect(sent[0]?.title).toBe('Atlas · Review')
    expect(sent[0]?.options).toEqual({ body: 'notifications.kind.reply', tag: 'memoh:s1' })
    sent[0]?.onclick?.()
    expect(deps.push).toHaveBeenCalledWith('/bot/b1?session=s1')
  })

  it('uses the desktop bridge and updates the badge even when notifications are off', async () => {
    const notify = vi.fn(async () => {})
    const setBadgeCount = vi.fn(async () => {})
    window.api = { desktop: { notify, setBadgeCount } }
    settings.desktop = true
    await emit()
    expect(notify).toHaveBeenCalledWith(expect.objectContaining({ sessionId: 's1', title: 'Atlas · Review' }))
    expect(sent).toHaveLength(0)
    settings.active = false
    unread.total = 3
    await flush()
    expect(setBadgeCount).toHaveBeenCalledWith(3)
  })

  it('cancels delivery if the scope ends before the asynchronous check completes', async () => {
    unread.entries.s1 = entry
    deps.listener?.(entry)
    scope.stop()
    await flush()
    expect(sent).toHaveLength(0)
  })
})
