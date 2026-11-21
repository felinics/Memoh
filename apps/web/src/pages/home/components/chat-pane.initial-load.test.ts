// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, reactive, ref, type App } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import type { UIStreamEventHandler, UITurn, WSClientMessage } from '@/composables/api/useChat'
import { isSessionViewed } from '@/composables/useSessionViewing'
import { useAttentionFocusStore } from '@/store/attention-focus'
import { useChatStore } from '@/store/chat-list'

const unread = vi.hoisted(() => {
  for (const name of ['localStorage', 'sessionStorage']) {
    const store = new Map<string, string>()
    Object.defineProperty(globalThis, name, {
      configurable: true,
      value: {
        getItem: (key: string) => store.get(key) ?? null,
        setItem: (key: string, val: string) => { store.set(key, String(val)) },
        removeItem: (key: string) => { store.delete(key) },
        clear: () => { store.clear() },
      },
    })
  }
  globalThis.fetch = vi.fn(async () => new Response(JSON.stringify({ items: [] }), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  })) as typeof fetch
  return {
    entries: {} as Record<string, { attentionAt: string, marked?: boolean }>,
    markRead: vi.fn(),
  }
})

const routeState = vi.hoisted(() => ({
  value: { name: 'bot', params: { botName: 'bot-1' }, query: {} as Record<string, string> },
}))

const api = vi.hoisted(() => ({
  createSession: vi.fn(),
  deleteSession: vi.fn(),
  forkSessionFromTurn: vi.fn(),
  fetchSession: vi.fn(),
  fetchSessions: vi.fn(),
  fetchBots: vi.fn(),
  fetchMessagesUI: vi.fn(),
  executeQuickAction: vi.fn(),
  fetchSafeSkillCatalog: vi.fn(async () => []),
  ensureACPRuntime: vi.fn(),
  fetchACPAgentOptions: vi.fn(),
  setACPRuntimeMode: vi.fn(),
  setACPRuntimeModel: vi.fn(),
  setACPRuntimeReasoning: vi.fn(),
  streamBotSessionsActivityEvents: vi.fn(async () => {}),
  connectWebSocket: vi.fn(),
  locateMessageUI: vi.fn(),
  enqueueSteerQueue: vi.fn(),
  enqueueFollowUpQueue: vi.fn(),
}))

vi.mock('@/store/unread', () => ({
  useUnreadStore: () => unread,
}))

vi.mock('@/composables/api/useChat', async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>()
  const stubbed = Object.fromEntries(
    Object.entries(actual).map(([key, val]) => [
      key,
      typeof val === 'function' ? vi.fn(async () => null) : val,
    ]),
  )
  return {
    ...stubbed,
    ...api,
  }
})

vi.mock('@memohai/sdk', async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>()
  const arrayEndpoints = new Set(['getModels', 'getProviders', 'getAcpProfiles'])
  return Object.fromEntries(
    Object.entries(actual).map(([key, val]) => [
      key,
      typeof val === 'function'
        ? vi.fn(async () => ({
            data: arrayEndpoints.has(key) ? [] : { items: [], targets: [], models: [] },
          }))
        : val,
    ]),
  )
})

vi.mock('vue-router', () => ({
  useRoute: () => routeState.value,
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
    currentRoute: ref(routeState.value),
  }),
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
      te: () => true,
      locale: ref('en'),
      availableLocales: ['en', 'zh', 'ja'],
    }),
  }
})

// Keep leaf subtrees lightweight so the test exercises ChatPane's real script,
// template banners/empty states, useSessionViewing, and attentionFocus logic.
vi.mock('./message-item.vue', async () => {
  const { h } = await import('vue')
  return {
    default: (props: { message: { id: string } }) => h('div', { 'data-test-message': props.message.id }, [
      h('div', { 'data-slot': 'notify-notice' }),
    ]),
  }
})
vi.mock('./composer-dock.vue', () => ({ default: { render: () => null } }))
vi.mock('./chat-scroll-rail.vue', () => ({ default: { render: () => null } }))
vi.mock('./media-gallery-lightbox.vue', () => ({ default: { render: () => null } }))
vi.mock('./chat-fork-dialog.vue', () => ({ default: { render: () => null } }))
vi.mock('./chat-example-suggestions.vue', () => ({ default: { render: () => null } }))
vi.mock('./composer-continue-on.vue', () => ({ default: { render: () => null } }))
vi.mock('./composer-folder-menu.vue', () => ({ default: { render: () => null } }))
vi.mock('./composer-connectors-menu.vue', () => ({ default: { render: () => null } }))
vi.mock('./composer-model-menu.vue', () => ({ default: { render: () => null } }))
vi.mock('./session-info-ring.vue', () => ({ default: { render: () => null } }))
vi.mock('./session-follow-up-queue.vue', () => ({ default: { render: () => null } }))
vi.mock('./codex-goal-bar.vue', () => ({ default: { render: () => null } }))
vi.mock('@/components/file-drop-overlay/index.vue', () => ({ default: { render: () => null } }))

vi.stubGlobal('ResizeObserver', class {
  observe() {}
  unobserve() {}
  disconnect() {}
})
vi.stubGlobal('IntersectionObserver', class {
  observe() {}
  unobserve() {}
  disconnect() {}
})

import ChatPane from './chat-pane.vue'

async function flush() {
  for (let i = 0; i < 6; i += 1) {
    await new Promise(resolve => setTimeout(resolve, 0))
    await nextTick()
  }
}

describe('ChatPane initial load and summary failure states', () => {
  let app: App | null = null
  let host: HTMLDivElement | null = null

  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
    localStorage.setItem('token', 'test-token')
    unread.entries = reactive({})
    unread.markRead.mockClear()
    routeState.value = reactive({ name: 'bot', params: { botName: 'bot-1' }, query: {} })
    vi.spyOn(document, 'hasFocus').mockReturnValue(true)
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    Element.prototype.scrollIntoView = vi.fn()
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: vi.fn(() => ({
        matches: true,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      })),
    })

    api.fetchBots.mockResolvedValue([{ id: 'bot-1', status: 'active', name: 'Bot' }])
    api.fetchSessions.mockResolvedValue({ items: [], nextCursor: null })
    api.connectWebSocket.mockImplementation((botId: string, handler: UIStreamEventHandler) => ({
      connected: true,
      send: vi.fn((message: WSClientMessage) => {
        if (message.type === 'runtime_subscribe') {
          queueMicrotask(() => {
            handler({
              type: 'runtime_snapshot',
              session_id: message.session_id,
              epoch: 'epoch-1',
              seq: 1,
              snapshot: {
                bot_id: botId,
                session_id: message.session_id,
                epoch: 'epoch-1',
                seq: 1,
                updated_at: '2026-07-27T08:00:00.000Z',
              },
            })
          })
        }
      }),
      abort: vi.fn(),
      forget: vi.fn(),
      bindSession: vi.fn(),
      close: vi.fn(),
      onOpen: null,
      onClose: null,
    }))
  })

  afterEach(() => {
    app?.unmount()
    host?.remove()
    app = null
    host = null
  })

  async function mountChatPane(sessionId: string, options: { noticeSessionId?: string } = {}) {
    const pinia = createPinia()
    setActivePinia(pinia)
    const store = useChatStore(pinia)
    const attentionFocus = useAttentionFocusStore(pinia)
    if (options.noticeSessionId) {
      attentionFocus.noticeSessionId = options.noticeSessionId
    }
    await store.selectBot('bot-1')

    host = document.createElement('div')
    document.body.append(host)
    app = createApp(defineComponent({
      setup() {
        store.bindChatView('chat:1', {
          botId: 'bot-1',
          sessionId,
          viewId: 'chat:1',
        }, true)
        store.focusChatView('chat:1')
        return () => h(ChatPane, {
          tabId: 'chat:1',
          sessionId,
          visible: true,
          active: true,
        })
      },
    }))
    app.use(pinia).use(PiniaColada)
    app.config.globalProperties.$t = (key: string) => key
    app.mount(host)
    await flush()
    return { root: host, store, attentionFocus }
  }

  it('does not mark an unread session read or clear attention focus when initial history load fails, until retry succeeds', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockRejectedValueOnce(new Error('network down'))
    unread.entries['session-1'] = { attentionAt: '2026-07-27T08:00:00.000Z' }
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})

    try {
      const { root, attentionFocus } = await mountChatPane('session-1', { noticeSessionId: 'session-1' })

      expect(root.textContent).toContain('common.loadFailed')
      expect(unread.markRead).not.toHaveBeenCalled()
      expect(isSessionViewed('session-1')).toBe(false)
      expect(attentionFocus.noticeSessionId).toBe('session-1')

      const notifyTurn: UITurn = {
        id: 'turn-1',
        turn_id: 'turn-1',
        role: 'assistant',
        timestamp: '2026-07-27T08:00:01.000Z',
        messages: [{
          id: 1,
          type: 'tool',
          name: 'notify_user',
          input: { message: 'done' },
          tool_call_id: 'call-1',
          running: false,
        }],
      }
      api.fetchMessagesUI.mockResolvedValueOnce([notifyTurn])

      const retryButton = [...root.querySelectorAll('button')].find(btn => btn.textContent?.includes('common.retry'))
      expect(retryButton).toBeDefined()
      retryButton!.click()
      await flush()

      expect(root.textContent).not.toContain('common.loadFailed')
      expect(unread.markRead).toHaveBeenCalledWith('session-1')
      expect(isSessionViewed('session-1')).toBe(true)
      expect(attentionFocus.noticeSessionId).toBeNull()
    } finally {
      consoleError.mockRestore()
    }
  })

  it('does not mark an unread session read when session summary load fails (or while a retry fails), until summary retry succeeds', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [], nextCursor: null })
    api.fetchSession.mockRejectedValueOnce(new Error('summary 500'))
    let resolveMessages!: (turns: UITurn[]) => void
    api.fetchMessagesUI.mockImplementationOnce(() => new Promise((resolve) => {
      resolveMessages = resolve
    }))
    unread.entries['session-deep'] = { attentionAt: '2026-07-27T08:00:00.000Z' }

    const { root, attentionFocus } = await mountChatPane('session-deep', { noticeSessionId: 'session-deep' })
    // Summary has already failed before messages finish loading.
    resolveMessages([{
      id: 'turn-1',
      turn_id: 'turn-1',
      role: 'assistant',
      timestamp: '2026-07-27T08:00:00.000Z',
      messages: [{
        id: 1,
        type: 'tool',
        name: 'notify_user',
        input: { message: 'done' },
        tool_call_id: 'call-1',
        running: false,
      }],
    }])
    await flush()

    expect(root.textContent).toContain('common.loadFailed')
    expect(unread.markRead).not.toHaveBeenCalled()
    expect(isSessionViewed('session-deep')).toBe(false)
    expect(attentionFocus.noticeSessionId).toBe('session-deep')

    // Clicking retry when summary still fails must not transiently mark read either.
    api.fetchSession.mockRejectedValueOnce(new Error('summary still 500'))
    const retryButton = [...root.querySelectorAll('button')].find(btn => btn.textContent?.includes('common.retry'))
    expect(retryButton).toBeDefined()
    retryButton!.click()
    await flush()

    expect(unread.markRead).not.toHaveBeenCalled()
    expect(isSessionViewed('session-deep')).toBe(false)
    expect(attentionFocus.noticeSessionId).toBe('session-deep')

    // Now summary retry succeeds -> session becomes readable and marks read.
    api.fetchSession.mockResolvedValueOnce({
      id: 'session-deep',
      bot_id: 'bot-1',
      title: 'Deep Linked',
      type: 'chat',
    })
    const retryAgainButton = [...root.querySelectorAll('button')].find(btn => btn.textContent?.includes('common.retry'))
    expect(retryAgainButton).toBeDefined()
    retryAgainButton!.click()
    await flush()

    expect(root.textContent).not.toContain('common.loadFailed')
    expect(unread.markRead).toHaveBeenCalledWith('session-deep')
    expect(isSessionViewed('session-deep')).toBe(true)
    expect(attentionFocus.noticeSessionId).toBeNull()
  })

  it('suppresses chat.emptySystemSession on a read-only session when initial load failed, and shows it if retry returns empty', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'sched-1', bot_id: 'bot-1', title: 'Schedule Run', type: 'schedule' }],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockRejectedValueOnce(new Error('network down'))
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})

    try {
      const { root } = await mountChatPane('sched-1')

      expect(root.textContent).toContain('common.loadFailed')
      expect(root.textContent).not.toContain('chat.emptySystemSession')

      api.fetchMessagesUI.mockResolvedValueOnce([])
      const retryButton = [...root.querySelectorAll('button')].find(btn => btn.textContent?.includes('common.retry'))
      expect(retryButton).toBeDefined()
      retryButton!.click()
      await flush()

      expect(root.textContent).not.toContain('common.loadFailed')
      expect(root.textContent).toContain('chat.emptySystemSession')
    } finally {
      consoleError.mockRestore()
    }
  })
})
