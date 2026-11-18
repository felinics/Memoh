// @vitest-environment jsdom
import { nextTick, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useChatSelectionStore } from './chat-selection'
import { DISPLAY_PANEL_ID, useWorkspaceTabsStore } from './workspace-tabs'
import { appKeyboardCommands, createKeyboardCommandRegistry } from '@/lib/keyboard-commands'
import { registerWorkbenchCommands } from '@/pages/home/commands/workbench-commands'
import { createEmptyWorkbenchState, findAdjacentGroupId, findGroupOfTab, insertTab, listGroups, splitGroupWithTab } from '@/workbench/tree'
import i18n, { prepareLocale } from '@/i18n'

const terminalCacheMocks = vi.hoisted(() => {
  class MemoryStorage implements Storage {
    private readonly values = new Map<string, string>()
    get length() { return this.values.size }
    clear() { this.values.clear() }
    getItem(key: string) { return this.values.get(key) ?? null }
    key(index: number) { return Array.from(this.values.keys())[index] ?? null }
    removeItem(key: string) { this.values.delete(key) }
    setItem(key: string, value: string) { this.values.set(key, String(value)) }
  }
  const local = new MemoryStorage()
  const session = new MemoryStorage()
  Object.defineProperty(globalThis, 'localStorage', { value: local, configurable: true, writable: true })
  Object.defineProperty(globalThis, 'sessionStorage', { value: session, configurable: true, writable: true })
  if (typeof window !== 'undefined') {
    Object.defineProperty(window, 'localStorage', { value: local, configurable: true, writable: true })
    Object.defineProperty(window, 'sessionStorage', { value: session, configurable: true, writable: true })
  }
  return {
    clearTerminalSnapshots: vi.fn(),
    clearTerminalSnapshotsForBot: vi.fn(),
    deleteTerminalSnapshot: vi.fn(),
    terminalCacheKey: (botId: string, panelId: string) => `${botId}::${panelId}`,
  }
})

vi.mock('@/composables/useTerminalCache', () => terminalCacheMocks)

const mobileBreakpoint = vi.hoisted(() => {
  const listeners = new Set<(event: { matches: boolean }) => void>()
  const state = { mobile: false }
  Object.defineProperty(globalThis.window, 'matchMedia', {
    configurable: true,
    writable: true,
    value: (query: string) => ({
      media: query,
      get matches() {
        return state.mobile
      },
      addEventListener: (_type: string, listener: (event: { matches: boolean }) => void) => {
        listeners.add(listener)
      },
      removeEventListener: (_type: string, listener: (event: { matches: boolean }) => void) => {
        listeners.delete(listener)
      },
      dispatchEvent: () => true,
    }),
  })
  return {
    setMobile(next: boolean) {
      state.mobile = next
      for (const listener of [...listeners]) listener({ matches: next })
    },
    reset() {
      state.mobile = false
      listeners.clear()
    },
  }
})

const chatStoreMock = vi.hoisted(() => ({
  sessionId: null as string | null,
  hasExplicitSessionSelection: false,
  loadingChats: false,
  tentativeIds: new Set<string>(),
  sessions: [] as Array<{ id: string, title?: string }>,
  knownSessions: [] as Array<{ id: string, title?: string, type?: string, route_metadata?: Record<string, unknown> }>,
  bots: [
    { id: 'bot-1', current_user_permissions: ['manage', 'workspace_exec', 'workspace_read'] },
    { id: 'bot-without-layout', current_user_permissions: ['manage', 'workspace_exec', 'workspace_read'] },
    { id: 'bot-chat-only', current_user_permissions: ['chat'] },
  ] as Array<{ id: string, current_user_permissions: string[] }>,
  deletedSession: undefined as unknown as ReturnType<typeof ref<{ id: string, botId: string, seq: number, composerScope?: string } | null>>,
  draftViewRequested: undefined as unknown as ReturnType<typeof ref<{
    botId: string
    viewId: string
    expectedSessionId: string | null
    explicitSelection: boolean
    input: Record<string, unknown> | null
    activate: boolean
    seq: number
  } | null>>,
  applyDraftViewRequest: vi.fn(),
  forkedSessionRequested: undefined as unknown as ReturnType<typeof ref<{
    botId: string
    viewId: string
    expectedSessionId: string
    sessionId: string
    title: string
    explicitSelection: true
    activate: boolean
    seq: number
  } | null>>,
  userSentInSession: undefined as unknown as ReturnType<typeof ref<{
    id: string
    botId: string
    viewId: string
    wasDraft: boolean
    seq: number
  } | null>>,
  draftPromoted: undefined as unknown as ReturnType<typeof ref<{
    botId: string
    viewId: string
    sessionId: string
    seq: number
  } | null>>,
  focusChatView: vi.fn(),
  selectSession: vi.fn(async (sessionId: string, options?: { explicitSelection?: boolean }) => {
    useChatSelectionStore().setSession(sessionId, options)
    chatStoreMock.sessionId = sessionId
    chatStoreMock.hasExplicitSessionSelection = options?.explicitSelection !== false
  }),
  selectDraft: vi.fn((options?: { explicitSelection?: boolean }) => {
    useChatSelectionStore().setSession(null, options)
    chatStoreMock.sessionId = null
    chatStoreMock.hasExplicitSessionSelection = options?.explicitSelection === true
  }),
  resetToEmptyComposer: vi.fn((options?: {
    clearPendingExternalAgent?: boolean
    explicitSelection?: boolean
    draftIntent?: boolean
  }) => {
    useChatSelectionStore().setSession(null, { explicitSelection: options?.explicitSelection === true })
    chatStoreMock.sessionId = null
    chatStoreMock.hasExplicitSessionSelection = options?.explicitSelection === true
  }),
  knownSessionSummary: vi.fn((sessionId: string) =>
    chatStoreMock.knownSessions.find(s => s.id === sessionId)
    ?? chatStoreMock.sessions.find(s => s.id === sessionId)
    ?? null,
  ),
  isSessionTentative: vi.fn((sessionId: string) => chatStoreMock.tentativeIds.has(sessionId)),
}))

vi.mock('@/store/chat-list', () => ({
  useChatStore: () => ({
    get sessionId() { return chatStoreMock.sessionId },
    get hasExplicitSessionSelection() { return chatStoreMock.hasExplicitSessionSelection },
    get loadingChats() { return chatStoreMock.loadingChats },
    sessions: chatStoreMock.sessions,
    knownSessions: chatStoreMock.knownSessions,
    get bots() { return chatStoreMock.bots },
    get userSentInSession() { return chatStoreMock.userSentInSession.value },
    get draftPromoted() { return chatStoreMock.draftPromoted.value },
    get draftViewRequested() { return chatStoreMock.draftViewRequested.value },
    get forkedSessionRequested() { return chatStoreMock.forkedSessionRequested.value },
    get deletedSession() { return chatStoreMock.deletedSession.value },
    applyDraftViewRequest: chatStoreMock.applyDraftViewRequest,
    focusChatView: chatStoreMock.focusChatView,
    selectSession: chatStoreMock.selectSession,
    selectDraft: chatStoreMock.selectDraft,
    resetToEmptyComposer: chatStoreMock.resetToEmptyComposer,
    knownSessionSummary: chatStoreMock.knownSessionSummary,
    isSessionTentative: chatStoreMock.isSessionTentative,
  }),
}))

async function flushTicks() {
  await nextTick()
  await Promise.resolve()
  await nextTick()
}

describe('useWorkspaceTabsStore (clean-slate workbench)', () => {
  beforeEach(async () => {
    await Promise.all([prepareLocale('en'), prepareLocale('zh'), prepareLocale('ja')])
    i18n.global.locale.value = 'en'
    localStorage.clear()
    sessionStorage.clear()
    mobileBreakpoint.reset()
    vi.clearAllMocks()

    chatStoreMock.sessionId = null
    chatStoreMock.hasExplicitSessionSelection = false
    chatStoreMock.loadingChats = false
    chatStoreMock.tentativeIds.clear()
    chatStoreMock.sessions.splice(0)
    chatStoreMock.knownSessions.splice(0)
    chatStoreMock.bots = [
      { id: 'bot-1', current_user_permissions: ['manage', 'workspace_exec', 'workspace_read'] },
      { id: 'bot-without-layout', current_user_permissions: ['manage', 'workspace_exec', 'workspace_read'] },
      { id: 'bot-chat-only', current_user_permissions: ['chat'] },
    ]
    chatStoreMock.deletedSession = ref(null)
    chatStoreMock.draftViewRequested = ref(null)
    chatStoreMock.forkedSessionRequested = ref(null)
    chatStoreMock.userSentInSession = ref(null)
    chatStoreMock.draftPromoted = ref(null)

    setActivePinia(createPinia())
    useChatSelectionStore().setBot('bot-1')
  })

  afterEach(() => {
    mobileBreakpoint.reset()
  })

  it('drops retired local/session storage keys on startup and does not navigate when unmounted', () => {
    localStorage.setItem('workspace-tabs', '{"legacy":true}')
    localStorage.setItem('workspace-panes', '{"legacy":true}')
    localStorage.setItem('workspace-layout', '{"legacy":true}')
    const store = useWorkspaceTabsStore()
    expect(localStorage.getItem('workspace-tabs')).toBeNull()
    expect(localStorage.getItem('workspace-panes')).toBeNull()
    expect(localStorage.getItem('workspace-layout')).toBeNull()
    expect(store.focusAdjacentTab(1)).toBe(false)
    expect(store.activeId).toBeNull()
  })

  it('spawns a default draft chat on cold start and prevents closing the sole draft chat', async () => {
    const store = useWorkspaceTabsStore()
    store.mountWorkspace()
    await flushTicks()

    expect(store.activeId).toBe('chat:1')
    expect(store.resolveTabTitle('chat:1')).toBe('New Session')
    store.closeTab('chat:1')
    expect(store.workbench.tabs['chat:1']).toBeDefined()
  })

  it('replaces preview chat/file tabs in place, promotes on edit/send/keepOpen, and protects pinned tabs from Close All', async () => {
    const store = useWorkspaceTabsStore()
    store.mountWorkspace()
    await flushTicks()

    // Single-click file replaces the initial preview draft chat in place.
    store.openFile('/data/a.ts')
    expect(store.workbench.tabs['chat:1']).toBeUndefined()
    expect(store.activeId).toBe('file:/data/a.ts')
    expect(store.isPreviewTab('file:/data/a.ts')).toBe(true)

    // Another single-click file replaces file:/data/a.ts in place.
    store.openFile('/data/b.ts')
    expect(store.workbench.tabs['file:/data/a.ts']).toBeUndefined()
    expect(store.activeId).toBe('file:/data/b.ts')

    // Double-click keepOpen promotes b.ts -> next preview c.ts opens beside it.
    store.keepOpenTab('file:/data/b.ts')
    expect(store.isPreviewTab('file:/data/b.ts')).toBe(false)
    store.openFile('/data/c.ts')
    expect(store.workbench.tabs['file:/data/b.ts']).toBeDefined()
    expect(store.workbench.tabs['file:/data/c.ts']).toBeDefined()

    // Pin b.ts -> moves to front and survives closeAllTabsInGroup.
    store.pinTab('file:/data/b.ts')
    const groupId = store.activeGroupId!
    store.closeAllTabsInGroup(groupId)
    expect(store.workbench.tabs['file:/data/b.ts']).toBeDefined()
    expect(store.workbench.tabs['file:/data/c.ts']).toBeUndefined()
  })

  it('splits subagent sessions to the right of the primary pane and aggregates subsequent subagents into that right group', async () => {
    const store = useWorkspaceTabsStore()
    store.mountWorkspace()
    await flushTicks()

    const primaryGroupId = store.spatialFirstGroupId!
    store.openSubagentSession({ sessionId: 'sub-1', title: 'Researcher' })
    expect(listGroups(store.workbench.root)).toHaveLength(2)

    const rightGroupId = findAdjacentGroupId(store.workbench.root, primaryGroupId, 'right')
    expect(rightGroupId).toBeTruthy()
    expect(findGroupOfTab(store.workbench.root, store.activeId!)?.id).toBe(rightGroupId)

    store.openSubagentSession({ sessionId: 'sub-2', title: 'Verifier' })
    expect(listGroups(store.workbench.root)).toHaveLength(2)
    expect(findGroupOfTab(store.workbench.root, store.activeId!)?.id).toBe(rightGroupId)
  })

  it('handles draftPromoted, userSentInSession, delayed /new draftViewRequested, and forkedSessionRequested in the originating split', async () => {
    const store = useWorkspaceTabsStore()
    store.mountWorkspace()
    await flushTicks()

    // Split chat to right so we have two chat views: chat:1 (left) and chat:2 (right).
    store.splitGroup(store.activeGroupId!, 'right')
    const leftId = 'chat:1'
    const rightId = 'chat:2'
    expect(store.workbench.tabs[leftId]).toBeDefined()
    expect(store.workbench.tabs[rightId]).toBeDefined()

    // Promote left draft to s-left and send message -> left becomes non-preview.
    chatStoreMock.draftPromoted.value = { botId: 'bot-1', viewId: leftId, sessionId: 's-left', seq: 1 }
    expect(store.workbench.tabs[leftId]?.target).toMatchObject({ kind: 'chat', sessionId: 's-left' })

    chatStoreMock.userSentInSession.value = { id: 's-left', botId: 'bot-1', viewId: leftId, wasDraft: false, seq: 1 }
    await flushTicks()
    expect(store.isPreviewTab(leftId)).toBe(false)

    // Focus right split, then resolve a delayed Fork requested from leftId -> opens in left group without stealing right focus.
    store.activateTab(rightId)
    const leftGroupId = findGroupOfTab(store.workbench.root, leftId)!.id
    chatStoreMock.forkedSessionRequested.value = {
      botId: 'bot-1',
      viewId: leftId,
      expectedSessionId: 's-left',
      sessionId: 's-forked',
      title: 'Forked Chat',
      explicitSelection: true,
      activate: false,
      seq: 1,
    }
    const forkedTab = Object.values(store.workbench.tabs).find(
      t => t.target.kind === 'chat' && t.target.sessionId === 's-forked',
    )
    expect(forkedTab).toBeDefined()
    expect(findGroupOfTab(store.workbench.root, forkedTab!.id)?.id).toBe(leftGroupId)
    expect(store.activeId).toBe(rightId)

    // Delayed `/new` on pinned leftId spawns a draft in leftGroupId and calls applyDraftViewRequest with new viewId.
    chatStoreMock.draftViewRequested.value = {
      botId: 'bot-1',
      viewId: leftId,
      expectedSessionId: 's-left',
      explicitSelection: false,
      input: null,
      activate: false,
      seq: 1,
    }
    expect(chatStoreMock.applyDraftViewRequest).toHaveBeenCalledOnce()
  })

  it('reconciles deleted sessions by closing tabs or resetting matching composerScope to draft', async () => {
    const store = useWorkspaceTabsStore()
    store.mountWorkspace()
    await flushTicks()

    store.openSessionChatPinned({ sessionId: 's-del-1', title: 'Delete Me' })
    store.openSessionChatPinned({ sessionId: 's-del-2', title: 'Reset Me' })
    const resetTab = Object.values(store.workbench.tabs).find(
      t => t.target.kind === 'chat' && t.target.sessionId === 's-del-2',
    )!

    chatStoreMock.deletedSession.value = { id: 's-del-1', botId: 'bot-1', seq: 1 }
    await flushTicks()
    expect(Object.values(store.workbench.tabs).some(t => t.target.kind === 'chat' && t.target.sessionId === 's-del-1')).toBe(false)

    chatStoreMock.deletedSession.value = {
      id: 's-del-2',
      botId: 'bot-1',
      seq: 2,
      composerScope: `bot-1:${resetTab.id}`,
    }
    await flushTicks()
    expect(store.workbench.tabs[resetTab.id]?.target).toMatchObject({ kind: 'chat', sessionId: null })
  })

  it('numbers terminals/browsers, resets numbering to 1 once all close, deduplicates browser URLs, and keeps display singleton', async () => {
    const store = useWorkspaceTabsStore()
    store.mountWorkspace()
    await flushTicks()

    store.openTerminal()
    store.openTerminal()
    expect(store.resolveTabTitle('terminal:1')).toBe('Terminal 1')
    expect(store.resolveTabTitle('terminal:2')).toBe('Terminal 2')

    store.closeTab('terminal:1')
    await flushTicks()
    expect(terminalCacheMocks.deleteTerminalSnapshot).toHaveBeenCalledWith('bot-1::terminal:1')

    store.openTerminal()
    expect(store.workbench.tabs['terminal:3']).toBeDefined()

    // Close all terminals -> counter restarts at 1.
    store.closeTab('terminal:2')
    store.closeTab('terminal:3')
    store.openTerminal()
    expect(store.workbench.tabs['terminal:1']).toBeDefined()

    // Browser URL deduplication.
    expect(store.openBrowserAt('http://localhost:3000/app')).toBe(true)
    const firstBrowserId = store.activeId!
    store.openTerminal()
    expect(store.openBrowserAt('localhost:3000/app')).toBe(true)
    expect(store.activeId).toBe(firstBrowserId)

    // Singleton display.
    store.openDisplay()
    store.openDisplay()
    expect(Object.keys(store.workbench.tabs).filter(id => id.startsWith('display:'))).toEqual([DISPLAY_PANEL_ID])
  })

  it('prunes disallowed tabs when bot permissions drop', async () => {
    const store = useWorkspaceTabsStore()
    store.mountWorkspace()
    await flushTicks()

    store.openFilePinned('/data/secret.ts')
    store.openTerminal()
    store.openBrowser()
    expect(store.workbench.tabs['file:/data/secret.ts']).toBeDefined()
    expect(store.workbench.tabs['terminal:1']).toBeDefined()
    expect(store.workbench.tabs['browser:1']).toBeDefined()

    useChatSelectionStore().setBot('bot-chat-only')
    await flushTicks()
    expect(store.workbench.tabs['file:/data/secret.ts']).toBeUndefined()
    expect(store.workbench.tabs['terminal:1']).toBeUndefined()
    expect(store.workbench.tabs['browser:1']).toBeUndefined()
  })

  it('queues dirty files for confirmation, aborts batch on cancel, and keeps tab open on save failure', async () => {
    const store = useWorkspaceTabsStore()
    store.mountWorkspace()
    await flushTicks()

    store.openFilePinned('/data/one.ts')
    store.openFilePinned('/data/two.ts')
    store.openFilePinned('/data/clean.ts')
    store.setFileDirty('file:/data/one.ts', true)
    store.setFileDirty('file:/data/two.ts', true)
    expect(store.dirtyFileCount).toBe(2)

    store.requestCloseTabs(['file:/data/one.ts', 'file:/data/two.ts', 'file:/data/clean.ts'])
    // Clean tab closes immediately; dirty ones queue in order.
    expect(store.workbench.tabs['file:/data/clean.ts']).toBeUndefined()
    expect(store.pendingClose).toEqual({ panelId: 'file:/data/one.ts', title: 'one.ts' })

    // Cancel aborts entire pending queue.
    await store.resolvePendingClose('cancel')
    expect(store.pendingClose).toBeNull()
    expect(store.workbench.tabs['file:/data/one.ts']).toBeDefined()
    expect(store.workbench.tabs['file:/data/two.ts']).toBeDefined()

    // Save failure keeps tab open, save success closes tab.
    store.registerFileSaveHandler('file:/data/one.ts', async () => false)
    store.registerFileSaveHandler('file:/data/two.ts', async () => {
      store.setFileDirty('file:/data/two.ts', false)
      return true
    })
    store.requestCloseTabs(['file:/data/one.ts', 'file:/data/two.ts'])
    await store.resolvePendingClose('save')
    expect(store.workbench.tabs['file:/data/one.ts']).toBeDefined()
    expect(store.pendingClose?.panelId).toBe('file:/data/two.ts')
    await store.resolvePendingClose('save')
    expect(store.workbench.tabs['file:/data/two.ts']).toBeUndefined()
  })

  it('cycles adjacent tabs and dispatches workbench keyboard shortcuts', async () => {
    const store = useWorkspaceTabsStore()
    store.mountWorkspace()
    await flushTicks()

    store.openFilePinned('/a.txt')
    store.openFilePinned('/b.txt')
    const registry = createKeyboardCommandRegistry()
    const dispose = registerWorkbenchCommands(registry, store)

    store.activateTab('file:/a.txt')
    registry.dispatch(appKeyboardCommands.nextWorkspaceTab)
    expect(store.activeId).toBe('file:/b.txt')
    registry.dispatch(appKeyboardCommands.previousWorkspaceTab)
    expect(store.activeId).toBe('file:/a.txt')

    registry.dispatch(appKeyboardCommands.splitWorkspaceRight)
    expect(listGroups(store.workbench.root)).toHaveLength(2)

    dispose()
  })

  describe('mobile projection & cold-start isolation', () => {
    it('preserves multi-column desktop split tree non-destructively across desktop -> mobile -> desktop resize', async () => {
      const store = useWorkspaceTabsStore()
      store.mountWorkspace()
      await flushTicks()

      store.openFilePinned('/data/left.ts')
      store.openFileToSide('/data/right.ts')
      expect(listGroups(store.workbench.root)).toHaveLength(2)
      expect(store.workbench.root.kind).toBe('split')
      store.setSplitRatio(store.workbench.root.id, 0.38)

      // Narrow to mobile (< 768px): splits remain preserved in state while UI projects single active tab.
      mobileBreakpoint.setMobile(true)
      await flushTicks()
      expect(store.isMobile).toBe(true)
      expect(listGroups(store.workbench.root)).toHaveLength(2)

      // Widen back to desktop (>= 768px): exact two-column split and 0.38 ratio remain intact.
      mobileBreakpoint.setMobile(false)
      await flushTicks()
      expect(listGroups(store.workbench.root)).toHaveLength(2)
      expect(store.workbench.root.kind === 'split' && store.workbench.root.ratio).toBeCloseTo(0.38, 5)
    })

    it('cold-starts cleanly on phone without overwriting persisted desktop layout and reuses single terminal/browser', async () => {
      let seeded = createEmptyWorkbenchState()
      seeded = insertTab(seeded, { id: 'file:/data/desk.ts', pinned: false, target: { kind: 'file', filePath: '/data/desk.ts' } }).state
      seeded = splitGroupWithTab(seeded, listGroups(seeded.root)[0]!.id, 'right', {
        id: 'file:/data/side.ts',
        pinned: false,
        target: { kind: 'file', filePath: '/data/side.ts' },
      })
      localStorage.setItem('memoh:workbench:v1', JSON.stringify({
        'bot-1': { workbench: seeded, terminalCounter: 0, browserCounter: 0, scheduleCounter: 0, chatCounter: 0 },
      }))

      mobileBreakpoint.setMobile(true)
      const store = useWorkspaceTabsStore()
      store.mountWorkspace()
      await flushTicks()

      expect(store.workbench.tabs['file:/data/desk.ts']).toBeUndefined()
      expect(listGroups(store.workbench.root)).toHaveLength(1)

      // Repeated terminal & browser opens on mobile reuse the existing tab rather than leaking unreachable shells.
      store.openTerminal()
      store.openTerminal()
      expect(Object.keys(store.workbench.tabs).filter(k => k.startsWith('terminal:'))).toEqual(['terminal:1'])

      store.openBrowser()
      store.activateChatPanel()
      store.openBrowser()
      expect(Object.keys(store.workbench.tabs).filter(k => k.startsWith('browser:'))).toEqual(['browser:1'])

      // Desktop layout seed in storage remains untouched.
      const saved = JSON.parse(sessionStorage.getItem('memoh:workbench:v1')!)
      expect(saved['bot-1'].workbench).toEqual(seeded)
    })
  })
})
