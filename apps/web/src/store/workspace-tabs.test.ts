// @vitest-environment jsdom
import { nextTick, ref, watch } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useChatSelectionStore } from './chat-selection'
import { DISPLAY_PANEL_ID, useWorkspaceTabsStore } from './workspace-tabs'
import { appKeyboardCommands, createKeyboardCommandRegistry } from '@/lib/keyboard-commands'
import { registerWorkbenchCommands } from '@/pages/home/commands/workbench-commands'
import { createEmptyWorkbenchState, findGroupOfTab, findRightNeighborOfSpatialFirst, insertTab, listGroups, splitGroupWithTab } from '@/workbench/tree'
import { activeWorkbenchDrag, startWorkbenchTabPointerGesture } from '@/workbench/drag'
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
    expect(findGroupOfTab(store.workbench.root, 'file:/data/a.ts')?.previewTabId).toBe('file:/data/a.ts')

    // Another single-click file replaces file:/data/a.ts in place.
    store.openFile('/data/b.ts')
    expect(store.workbench.tabs['file:/data/a.ts']).toBeUndefined()
    expect(store.activeId).toBe('file:/data/b.ts')

    // Double-click keepOpen promotes b.ts -> next preview c.ts opens beside it.
    store.keepOpenTab('file:/data/b.ts')
    expect(findGroupOfTab(store.workbench.root, 'file:/data/b.ts')?.previewTabId).toBeNull()
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

    store.openSubagentSession({ sessionId: 'sub-1', title: 'Researcher' })
    expect(listGroups(store.workbench.root)).toHaveLength(2)

    const rightGroupId = findRightNeighborOfSpatialFirst(store.workbench.root)
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
    expect(findGroupOfTab(store.workbench.root, leftId)?.previewTabId).not.toBe(leftId)

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
      seeded = insertTab(seeded, { id: 'file:/data/desk.ts', pinned: false, target: { kind: 'file', filePath: '/data/desk.ts' } })
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

    it('restores saved desktop split layout and merges mobile-opened tabs on mobile -> desktop widen', async () => {
      let seeded = createEmptyWorkbenchState()
      seeded = insertTab(seeded, { id: 'file:/data/desk.ts', pinned: false, target: { kind: 'file', filePath: '/data/desk.ts' } })
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

      // Open a terminal while cold-started on phone, then widen back to desktop.
      store.openTerminal()
      expect(store.workbench.tabs['terminal:1']).toBeDefined()

      mobileBreakpoint.setMobile(false)
      await flushTicks()

      expect(listGroups(store.workbench.root)).toHaveLength(2)
      expect(store.workbench.tabs['file:/data/desk.ts']).toBeDefined()
      expect(store.workbench.tabs['file:/data/side.ts']).toBeDefined()
      expect(store.workbench.tabs['terminal:1']).toBeDefined()

      // Persistence is now active on desktop again.
      store.openFilePinned('/data/after-widen.ts')
      await flushTicks()
      const saved = JSON.parse(sessionStorage.getItem('memoh:workbench:v1')!)
      expect(saved['bot-1'].workbench.tabs['file:/data/after-widen.ts']).toBeDefined()
    })

    it('prevents mobile panel actions from regressing desktop counters or colliding on return to desktop', async () => {
      let seeded = createEmptyWorkbenchState('g1')
      seeded = insertTab(seeded, { id: 'terminal:2', pinned: false, target: { kind: 'terminal', instance: 2 } }, { groupId: 'g1' })
      localStorage.setItem('memoh:workbench:v1', JSON.stringify({
        'bot-1': { workbench: seeded, terminalCounter: 2, browserCounter: 0, scheduleCounter: 0, chatCounter: 0 },
      }))

      mobileBreakpoint.setMobile(true)
      const store = useWorkspaceTabsStore()
      store.mountWorkspace()
      await flushTicks()

      store.openTerminal()
      expect(store.workbench.tabs['terminal:3']).toBeDefined()

      // Widening back preserves both terminal:2 and terminal:3, and splitting terminal:2 creates terminal:4 instead of a no-op.
      mobileBreakpoint.setMobile(false)
      await flushTicks()

      expect(store.workbench.tabs['terminal:2']).toBeDefined()
      expect(store.workbench.tabs['terminal:3']).toBeDefined()
      const ownerGroup = findGroupOfTab(store.workbench.root, 'terminal:2')!
      store.activateTab('terminal:2')
      store.splitGroup(ownerGroup.id, 'right')
      expect(listGroups(store.workbench.root).length).toBeGreaterThanOrEqual(2)
      expect(store.workbench.tabs['terminal:4']).toBeDefined()
    })

    it('cycles mobile tabs across preserved desktop groups and targets chat group for session opens', async () => {
      const store = useWorkspaceTabsStore()
      store.mountWorkspace()
      await flushTicks()

      const chatId = store.activeId!
      store.openFileToSide('/data/side.ts')
      expect(listGroups(store.workbench.root)).toHaveLength(2)
      expect(store.activeId).toBe('file:/data/side.ts')

      mobileBreakpoint.setMobile(true)
      await flushTicks()

      // Even though active group holds file:/data/side.ts alone, focusAdjacentTab cycles across groups on mobile.
      expect(store.focusAdjacentTab(1)).toBe(true)
      expect(store.activeId).toBe(chatId)
      expect(store.focusAdjacentTab(-1)).toBe(true)
      expect(store.activeId).toBe('file:/data/side.ts')

      // Selecting a session while focused on a file group on mobile routes to the chat group instead of evicting the file.
      store.openSessionChat({ sessionId: 's-mobile', title: 'Mobile Chat' })
      expect(store.workbench.tabs['file:/data/side.ts']).toBeDefined()
      expect(Object.values(store.workbench.tabs).some(t => t.target.kind === 'chat' && t.target.sessionId === 's-mobile')).toBe(true)
    })
  })

  describe('concurrency, bot isolation & synchronous draft refill', () => {
    it('keeps a fallback tab synchronously when closing the last open file tab', async () => {
      const store = useWorkspaceTabsStore()
      store.mountWorkspace()
      await flushTicks()

      // Capture every reactive workbench commit to verify tabs never drop to zero.
      const observedCounts: number[] = []
      const stop = watch(
        () => store.workbench,
        (wb) => { observedCounts.push(Object.keys(wb.tabs).length) },
        { flush: 'sync' },
      )

      store.openFile('/data/only.ts')
      store.closeTab('file:/data/only.ts')
      stop()

      expect(observedCounts.every(count => count > 0)).toBe(true)
      expect(Object.values(store.workbench.tabs).some(t => t.target.kind === 'chat')).toBe(true)
    })

    it('clears workbench and loadedBotId when currentBotId becomes null and clears stale drafts on legacy upgrade', async () => {
      localStorage.setItem('workspace-layout', '{"legacy":true}')
      localStorage.setItem('chat-input-drafts', JSON.stringify({ 'bot-1:chat:1': 'stale' }))
      const store = useWorkspaceTabsStore()
      expect(localStorage.getItem('chat-input-drafts')).toBeNull()

      store.mountWorkspace()
      await flushTicks()
      expect(store.loadedBotId).toBe('bot-1')

      useChatSelectionStore().setBot(null)
      await flushTicks()
      expect(store.loadedBotId).toBeNull()
      expect(Object.keys(store.workbench.tabs)).toHaveLength(0)
    })

    it('aborts in-flight save-and-close if cancelled or switched to another bot before save resolves', async () => {
      const store = useWorkspaceTabsStore()
      store.mountWorkspace()
      await flushTicks()

      store.openFilePinned('/data/slow.ts')
      store.setFileDirty('file:/data/slow.ts', true, 'bot-1')

      let resolveSave!: (ok: boolean) => void
      const saveHandler = vi.fn(() => new Promise<boolean>((resolve) => { resolveSave = resolve }))
      store.registerFileSaveHandler('file:/data/slow.ts', saveHandler, 'bot-1')
      store.requestCloseTab('file:/data/slow.ts')
      expect(store.pendingClose?.panelId).toBe('file:/data/slow.ts')

      const closingPromise = store.resolvePendingClose('save')
      await store.resolvePendingClose('cancel')
      resolveSave(true)
      await closingPromise

      expect(store.workbench.tabs['file:/data/slow.ts']).toBeDefined()
    })

    it('ignores stale bot dirty calls and mismatched unregisterFileSaveHandler calls', async () => {
      const store = useWorkspaceTabsStore()
      store.mountWorkspace()
      await flushTicks()

      store.openFilePinned('/data/shared.ts')
      store.setFileDirty('file:/data/shared.ts', true, 'bot-other')
      expect(store.fileDirty['file:/data/shared.ts']).toBeUndefined()

      const oldHandler = vi.fn(async () => false)
      const newHandler = vi.fn(async () => true)
      store.registerFileSaveHandler('file:/data/shared.ts', newHandler, 'bot-1')
      store.unregisterFileSaveHandler('file:/data/shared.ts', oldHandler)

      store.setFileDirty('file:/data/shared.ts', true, 'bot-1')
      store.requestCloseTab('file:/data/shared.ts')
      await store.resolvePendingClose('save')
      expect(newHandler).toHaveBeenCalledOnce()
      expect(oldHandler).not.toHaveBeenCalled()
      expect(store.workbench.tabs['file:/data/shared.ts']).toBeUndefined()
    })

    it('blocks cross-bot mutations before pre-flush switch completes, ignores other-bot deletions, promotes explicitSelection on pinned open, and does not re-queue cleaned tabs', async () => {
      const store = useWorkspaceTabsStore()
      store.mountWorkspace()
      await flushTicks()

      // Non-explicit session tab promoted to explicit when opened pinned
      store.openSessionChat({ sessionId: 's-non-exp', explicitSelection: false })
      expect(chatStoreMock.hasExplicitSessionSelection).toBe(false)
      store.openSessionChatPinned({ sessionId: 's-non-exp' })
      expect(chatStoreMock.hasExplicitSessionSelection).toBe(true)

      // Deleting a session belonging to another bot must not touch bot-1's tabs
      chatStoreMock.deletedSession.value = { id: 's-non-exp', botId: 'bot-other', seq: 99 }
      await flushTicks()
      expect(Object.values(store.workbench.tabs).some(t => t.target.kind === 'chat' && t.target.sessionId === 's-non-exp')).toBe(true)

      // Queued dirty tab that becomes clean before requestCloseTabs must not stay in closeQueue
      store.openFilePinned('/data/cleaned.ts')
      store.setFileDirty('file:/data/cleaned.ts', true, 'bot-1')
      store.requestCloseTab('file:/data/cleaned.ts')
      expect(store.pendingClose?.panelId).toBe('file:/data/cleaned.ts')
      store.setFileDirty('file:/data/cleaned.ts', false, 'bot-1')
      store.requestCloseTabs(['file:/data/cleaned.ts'])
      expect(store.workbench.tabs['file:/data/cleaned.ts']).toBeUndefined()
      expect(store.pendingClose).toBeNull()

      // Pre-flush bot switch divergence: synchronous mutations for bot-2 must not corrupt bot-1
      useChatSelectionStore().setBot('bot-without-layout')
      store.openTerminal()
      store.openSessionChat({ sessionId: 's-leaked' })
      expect(store.workbench.tabs['terminal:1']).toBeUndefined()
      expect(Object.values(store.workbench.tabs).some(t => t.target.kind === 'chat' && t.target.sessionId === 's-leaked')).toBe(false)
      await flushTicks()
      expect(store.loadedBotId).toBe('bot-without-layout')
    })

    it('reconciles previously deleted sessions before alignSelectionWithDock selects them on bot switch, and clears pendingDragPersist on resetBot', async () => {
      // Seed bot-2 with a stored chat tab pointing at a session that gets deleted while bot-1 is active.
      let bot2Wb = createEmptyWorkbenchState('g1')
      bot2Wb = insertTab(bot2Wb, { id: 'chat:1', pinned: false, target: { kind: 'chat', sessionId: 's-deleted', explicitSelection: true } })
      localStorage.setItem('memoh:workbench:v1', JSON.stringify({
        'bot-2': { workbench: bot2Wb, terminalCounter: 0, browserCounter: 0, scheduleCounter: 0, chatCounter: 1 },
      }))

      const store = useWorkspaceTabsStore()
      store.mountWorkspace()
      await flushTicks()

      // Record deletion for bot-2 while still on bot-1.
      chatStoreMock.deletedSession.value = { id: 's-deleted', botId: 'bot-2', seq: 1 }
      await flushTicks()
      chatStoreMock.selectSession.mockClear()

      // Switch to bot-2 -> deleted session tab must be removed BEFORE alignSelectionWithDock runs.
      useChatSelectionStore().setBot('bot-2')
      await flushTicks()
      expect(chatStoreMock.selectSession).not.toHaveBeenCalledWith('s-deleted', expect.anything())
      expect(chatStoreMock.selectSession).not.toHaveBeenCalledWith('s-deleted')
      expect(Object.values(store.workbench.tabs).some(t => t.target.kind === 'chat' && t.target.sessionId === 's-deleted')).toBe(false)

      // Mid-drag resetBot must clear pendingDragPersist so ending the drag does not write back a record.
      store.panelDragging = true
      store.openFilePinned('/data/dragged.ts')
      store.resetBot('bot-2')
      await flushTicks()
      const savedAfterReset = JSON.parse(sessionStorage.getItem('memoh:workbench:v1') || '{}')
      expect(savedAfterReset['bot-2']).toBeUndefined()
    })

    it('preserves explicit chatStore.sessionId when restore reconciles deleted tabs, and activates already-open tab dropped onto its own single-tab group split edge', async () => {
      // Seed bot-2 with only a deleted chat tab while chatStore explicitly points at a valid session.
      let bot2Wb = createEmptyWorkbenchState('g1')
      bot2Wb = insertTab(bot2Wb, { id: 'chat:1', pinned: false, target: { kind: 'chat', sessionId: 's-deleted', explicitSelection: true } })
      localStorage.setItem('memoh:workbench:v1', JSON.stringify({
        'bot-2': { workbench: bot2Wb, terminalCounter: 0, browserCounter: 0, scheduleCounter: 0, chatCounter: 1 },
      }))

      const store = useWorkspaceTabsStore()
      store.mountWorkspace()
      await flushTicks()

      chatStoreMock.deletedSession.value = { id: 's-deleted', botId: 'bot-2', seq: 1 }
      await flushTicks()

      // Switch to bot-2 with an explicit valid session already chosen on chatStore.
      chatStoreMock.sessionId = 's-valid'
      chatStoreMock.hasExplicitSessionSelection = true
      useChatSelectionStore().setBot('bot-2')
      await flushTicks()

      expect(Object.values(store.workbench.tabs).some(t => t.target.kind === 'chat' && t.target.sessionId === 's-valid')).toBe(true)
      expect(chatStoreMock.sessionId).toBe('s-valid')

      // External drop of an already-open tab onto its own inactive single-tab group's right split edge must still activate & select it.
      useChatSelectionStore().setBot('bot-1')
      await flushTicks()
      store.openSessionChatPinned({ sessionId: 's-valid' })
      store.openFileToSide('/data/side.ts')
      const groups = listGroups(store.workbench.root)
      expect(groups).toHaveLength(2)
      const chatGroup = groups.find(g => g.tabIds.some(id => store.workbench.tabs[id]?.target.kind === 'chat'))!
      const fileGroup = groups.find(g => g.id !== chatGroup.id)!
      store.activateGroup(fileGroup.id)
      expect(store.activeGroupId).toBe(fileGroup.id)

      chatStoreMock.selectSession.mockClear()
      store.dropExternalItem({ kind: 'session', sessionId: 's-valid' }, chatGroup.id, 'right')
      expect(store.activeGroupId).toBe(chatGroup.id)
      expect(chatStoreMock.selectSession).toHaveBeenCalledWith('s-valid')
    })

    it('skips pruning when permissions are absent or empty, and prunes once explicit permissions arrive', async () => {
      let wb = createEmptyWorkbenchState('g1')
      wb = insertTab(wb, { id: 'file:/data/a.ts', pinned: false, target: { kind: 'file', filePath: '/data/a.ts' } })
      wb = insertTab(wb, { id: 'terminal:1', pinned: false, target: { kind: 'terminal', instance: 1 } })
      localStorage.setItem('memoh:workbench:v1', JSON.stringify({
        'bot-unhydrated': { workbench: wb, terminalCounter: 1, browserCounter: 0, scheduleCounter: 0, chatCounter: 0 },
      }))

      chatStoreMock.bots.push({ id: 'bot-unhydrated', current_user_permissions: [] })
      useChatSelectionStore().setBot('bot-unhydrated')
      const store = useWorkspaceTabsStore()
      store.mountWorkspace()
      await flushTicks()

      // Empty permissions array during hydration must NOT wipe persisted file/terminal tabs.
      expect(store.workbench.tabs['file:/data/a.ts']).toBeDefined()
      expect(store.workbench.tabs['terminal:1']).toBeDefined()

      // Once real permissions arrive (chat-only), restricted tabs are pruned.
      const botEntry = chatStoreMock.bots.find(b => b.id === 'bot-unhydrated')!
      botEntry.current_user_permissions = ['chat']
      // Trigger reactivity by updating bots reference if needed or re-switching
      chatStoreMock.bots = [...chatStoreMock.bots]
      useChatSelectionStore().setBot('bot-1')
      await flushTicks()
      useChatSelectionStore().setBot('bot-unhydrated')
      await flushTicks()
      expect(store.workbench.tabs['file:/data/a.ts']).toBeUndefined()
      expect(store.workbench.tabs['terminal:1']).toBeUndefined()
    })

    it('passes activate=true to applyDraftViewRequest when a background draft request replaces the active preview tab', async () => {
      const store = useWorkspaceTabsStore()
      store.mountWorkspace()
      await flushTicks()

      // Keep chat:origin open, then open a preview file that becomes the group's active preview tab.
      store.openSessionChatPinned({ sessionId: 's-origin' })
      const originTabId = Object.keys(store.workbench.tabs).find((id) => {
        const tab = store.workbench.tabs[id]
        return tab?.target.kind === 'chat' && tab.target.sessionId === 's-origin'
      })!
      store.openFile('/data/preview.ts')
      expect(store.activeId).toBe('file:/data/preview.ts')

      chatStoreMock.applyDraftViewRequest.mockClear()
      chatStoreMock.draftViewRequested.value = {
        botId: 'bot-1',
        viewId: originTabId,
        expectedSessionId: 's-origin',
        explicitSelection: true,
        input: null,
        activate: false,
        seq: 1,
      }

      // Replacing the active preview tab makes the newly created draft tab active -> applyDraftViewRequest must receive true.
      const createdId = store.activeId!
      expect(createdId).not.toBe('file:/data/preview.ts')
      expect(store.workbench.tabs[createdId]?.target.kind).toBe('chat')
      expect(chatStoreMock.applyDraftViewRequest).toHaveBeenCalledWith(
        expect.objectContaining({ viewId: createdId }),
        true,
      )
    })

    it('preserves an active editing draft on mobile cold-start widen while dropping inactive cold-start drafts', async () => {
      let desktopWb = createEmptyWorkbenchState('g-desktop')
      desktopWb = insertTab(desktopWb, { id: 'file:/data/desk.ts', pinned: false, target: { kind: 'file', filePath: '/data/desk.ts' } })
      localStorage.setItem('memoh:workbench:v1', JSON.stringify({
        'bot-1': { workbench: desktopWb, terminalCounter: 0, browserCounter: 0, scheduleCounter: 0, chatCounter: 0 },
      }))

      // Cold-start on mobile -> gets fresh draft chat:1, user opens a file, then switches back to draft chat:1 to type.
      mobileBreakpoint.setMobile(true)
      const store = useWorkspaceTabsStore()
      store.mountWorkspace()
      await flushTicks()
      const mobileDraftId = store.activeId!
      expect(store.workbench.tabs[mobileDraftId]?.target.kind).toBe('chat')

      store.openFilePinned('/data/mobile.ts')
      store.activateTab(mobileDraftId)
      expect(store.activeId).toBe(mobileDraftId)

      // Widen back to desktop -> active editing draft must be preserved and remain active alongside desk.ts and mobile.ts.
      mobileBreakpoint.setMobile(false)
      await flushTicks()
      expect(store.workbench.tabs['file:/data/desk.ts']).toBeDefined()
      expect(store.workbench.tabs['file:/data/mobile.ts']).toBeDefined()
      expect(store.workbench.tabs[mobileDraftId]).toBeDefined()
      expect(store.activeId).toBe(mobileDraftId)
    })

    it('cancels active drag when entering mobile or removing the dragged tab, and blocks pre-flush tab mutations on bot switch', async () => {
      const store = useWorkspaceTabsStore()
      store.mountWorkspace()
      await flushTicks()
      store.openFilePinned('/data/a.ts')
      store.openFilePinned('/data/b.ts')
      const groupId = store.activeGroupId!

      const downEv = new MouseEvent('pointerdown', { button: 0, buttons: 1, clientX: 20, clientY: 20, bubbles: true, cancelable: true }) as PointerEvent
      Object.defineProperty(downEv, 'pointerId', { value: 1 })
      startWorkbenchTabPointerGesture({
        event: downEv,
        tabId: 'file:/data/b.ts',
        groupId,
        pinned: false,
        getTitle: () => 'b.ts',
        isMobile: () => store.isMobile,
        getGroupTabIds: gid => listGroups(store.workbench.root).find(g => g.id === gid)?.tabIds ?? [],
        isTabPinned: tid => Boolean(store.workbench.tabs[tid]?.pinned),
        onActivate: () => store.activateTab('file:/data/b.ts'),
        onDragStateChange: (d) => { store.panelDragging = d },
        onReorderInGroup: (gid, tid, idx) => store.reorderTab(gid, tid, idx),
        onCommitDrop: vi.fn(),
      })
      const moveEv = new MouseEvent('pointermove', { buttons: 1, clientX: 120, clientY: 120, bubbles: true, cancelable: true }) as PointerEvent
      Object.defineProperty(moveEv, 'pointerId', { value: 1 })
      window.dispatchEvent(moveEv)
      expect(activeWorkbenchDrag.value?.detachedTabId).toBe('file:/data/b.ts')

      // Removing the dragged tab cancels the active drag cleanly.
      store.closeTab('file:/data/b.ts')
      expect(activeWorkbenchDrag.value).toBeNull()
      expect(store.panelDragging).toBe(false)

      // Entering mobile while dragging cancels the active drag cleanly.
      const downEv2 = new MouseEvent('pointerdown', { button: 0, buttons: 1, clientX: 20, clientY: 20, bubbles: true, cancelable: true }) as PointerEvent
      Object.defineProperty(downEv2, 'pointerId', { value: 2 })
      startWorkbenchTabPointerGesture({
        event: downEv2,
        tabId: 'file:/data/a.ts',
        groupId,
        pinned: false,
        getTitle: () => 'a.ts',
        isMobile: () => store.isMobile,
        getGroupTabIds: gid => listGroups(store.workbench.root).find(g => g.id === gid)?.tabIds ?? [],
        isTabPinned: tid => Boolean(store.workbench.tabs[tid]?.pinned),
        onActivate: () => store.activateTab('file:/data/a.ts'),
        onDragStateChange: (d) => { store.panelDragging = d },
        onReorderInGroup: (gid, tid, idx) => store.reorderTab(gid, tid, idx),
        onCommitDrop: vi.fn(),
      })
      const moveEv2 = new MouseEvent('pointermove', { buttons: 1, clientX: 120, clientY: 120, bubbles: true, cancelable: true }) as PointerEvent
      Object.defineProperty(moveEv2, 'pointerId', { value: 2 })
      window.dispatchEvent(moveEv2)
      expect(activeWorkbenchDrag.value).not.toBeNull()
      mobileBreakpoint.setMobile(true)
      await flushTicks()
      expect(activeWorkbenchDrag.value).toBeNull()
      mobileBreakpoint.setMobile(false)
      await flushTicks()

      // Pre-flush bot switch blocks async/child title & session callbacks on stale bot-1 state.
      store.openTerminal()
      useChatSelectionStore().setBot('bot-without-layout')
      store.updateTerminalTitle('terminal:1', 'leaked-shell')
      expect(store.workbench.tabs['terminal:1']?.target).toEqual({ kind: 'terminal', instance: 1 })
      await flushTicks()
      expect(store.loadedBotId).toBe('bot-without-layout')
    })
  })
})
