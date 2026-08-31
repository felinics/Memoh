import { nextTick, reactive, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useChatSelectionStore } from './chat-selection'
import { useWorkspaceTabsStore } from './workspace-tabs'

vi.hoisted(() => {
  class MemoryStorage implements Storage {
    private readonly values = new Map<string, string>()

    get length() {
      return this.values.size
    }

    clear() {
      this.values.clear()
    }

    getItem(key: string) {
      return this.values.get(key) ?? null
    }

    key(index: number) {
      return Array.from(this.values.keys())[index] ?? null
    }

    removeItem(key: string) {
      this.values.delete(key)
    }

    setItem(key: string, value: string) {
      this.values.set(key, String(value))
    }
  }

  const listeners = new Map<string, Set<EventListenerOrEventListenerObject>>()
  const storage = new MemoryStorage()
  // Keep real i18n active so terminal-title assertions catch missing or
  // misspelled keys, while making the expected English fallback deterministic.
  storage.setItem('language', 'en')
  // Layout/selection now live in sessionStorage (per-Tab), so the store's
  // restore path reads it, not localStorage. A separate instance mirrors the
  // real browser split (two distinct areas under the same key names).
  const session = new MemoryStorage()
  Object.defineProperty(globalThis, 'localStorage', {
    value: storage,
    configurable: true,
    writable: true,
  })
  Object.defineProperty(globalThis, 'sessionStorage', {
    value: session,
    configurable: true,
    writable: true,
  })
  Object.defineProperty(globalThis, 'window', {
    value: {
      localStorage: storage,
      sessionStorage: session,
      addEventListener: (type: string, listener: EventListenerOrEventListenerObject) => {
        const set = listeners.get(type) ?? new Set<EventListenerOrEventListenerObject>()
        set.add(listener)
        listeners.set(type, set)
      },
      removeEventListener: (type: string, listener: EventListenerOrEventListenerObject) => {
        listeners.get(type)?.delete(listener)
      },
      dispatchEvent: (event: Event) => {
        for (const listener of listeners.get(event.type) ?? []) {
          if (typeof listener === 'function') {
            listener.call(globalThis.window, event)
          } else {
            listener.handleEvent(event)
          }
        }
        return true
      },
    },
    configurable: true,
    writable: true,
  })
})

// Controllable matchMedia for the mobile breakpoint (useIsMobile). The window
// mock above deliberately omits browser APIs until a test needs them; the
// store reads the query once at setup and subscribes to 'change', so tests
// drive both through setMobile. reset() runs between tests: a leaked
// mobile=true would flip every store created afterwards.
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
  sessions: [] as Array<{ id: string, title?: string }>,
  knownSessions: [] as Array<{ id: string, title?: string, type?: string }>,
  createNewSession: vi.fn(async () => {}),
  deletedSession: undefined as unknown as ReturnType<typeof ref<{ id: string, botId: string, seq: number, composerScope?: string } | null>>,
  pendingExternalAgentSessionInput: undefined as unknown as ReturnType<typeof ref<Record<string, unknown> | null>>,
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
  guiToolUseRequested: undefined as unknown as ReturnType<typeof ref<{
    botId: string
    sessionId: string
    toolCallId: string
    toolName: string
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
    chatStoreMock.knownSessions.find(session => session.id === sessionId)
    ?? chatStoreMock.sessions.find(session => session.id === sessionId)
    ?? null,
  ),
}))

vi.mock('@/store/chat-list', () => ({
  useChatStore: () => ({
    get sessionId() {
      return chatStoreMock.sessionId
    },
    get hasExplicitSessionSelection() {
      return chatStoreMock.hasExplicitSessionSelection
    },
    sessions: chatStoreMock.sessions,
    knownSessions: chatStoreMock.knownSessions,
    get loadingChats() {
      return chatStoreMock.loadingChats
    },
    activeSession: null,
    get userSentInSession() {
      return chatStoreMock.userSentInSession.value
    },
    get guiToolUseRequested() {
      return chatStoreMock.guiToolUseRequested.value
    },
    get pendingExternalAgentSessionInput() {
      return chatStoreMock.pendingExternalAgentSessionInput.value
    },
    get draftViewRequested() {
      return chatStoreMock.draftViewRequested.value
    },
    get forkedSessionRequested() {
      return chatStoreMock.forkedSessionRequested.value
    },
    get deletedSession() {
      return chatStoreMock.deletedSession.value
    },
    bots: [
      {
        id: 'bot-1',
        current_user_permissions: ['manage', 'workspace_exec', 'workspace_read'],
      },
      {
        id: 'bot-without-layout',
        current_user_permissions: ['manage', 'workspace_exec', 'workspace_read'],
      },
    ],
    isSessionStreaming: vi.fn(() => false),
    knownSessionSummary: chatStoreMock.knownSessionSummary,
    createNewSession: chatStoreMock.createNewSession,
    focusChatView: chatStoreMock.focusChatView,
    selectSession: chatStoreMock.selectSession,
    selectDraft: chatStoreMock.selectDraft,
    resetToEmptyComposer: chatStoreMock.resetToEmptyComposer,
    applyDraftViewRequest: chatStoreMock.applyDraftViewRequest,
  }),
}))

interface FakePanel {
  id: string
  component: string
  params: Record<string, unknown>
  title: string
  renderer?: string
  group?: FakeGroup
  api: {
    setActive: () => void
    close: () => void
    setTitle: (title: string) => void
    updateParameters: (params: Record<string, unknown>) => void
    moveTo: (options: { group?: FakeGroup; position?: 'top' | 'bottom' | 'left' | 'right' | 'center' }) => void
    readonly title: string
  }
}

interface FakeGroup {
  id: string
  element: HTMLElement
  // Mirrors DockviewGroupPanel.model: the store toggles header.hidden at
  // runtime for the mobile single stack (runtime-only, never persisted).
  model: { header: { hidden: boolean } }
  api: {
    getHeaderPosition: () => 'top' | 'bottom'
    setHeaderPosition: ReturnType<typeof vi.fn>
    setActivePanel: (panel: FakePanel) => void
    setActive: () => void
    setSize: ReturnType<typeof vi.fn>
    moveTo: (options: { group?: FakeGroup, position?: 'top' | 'bottom' | 'left' | 'right' | 'center' }) => void
  }
  readonly panels: FakePanel[]
  readonly activePanel: FakePanel | null
}

function createFakeDock() {
  const panels: FakePanel[] = []
  const groups: FakeGroup[] = []
  const groupActivePanels = new Map<string, FakePanel | null>()
  const groupPositions = new Map<string, { referenceGroup: string; direction: string }>()
  const removeListeners: Array<(panel: FakePanel) => void> = []
  // Drop-veto probes: the store subscribes veto handlers in registerApi; tests
  // fire synthetic events through dock.emitWillShowOverlay / emitWillDrop.
  interface FakeDropEvent {
    position: string
    group: FakeGroup
    getData: () => { panelId?: string | null } | undefined
    preventDefault: ReturnType<typeof vi.fn>
  }
  const willShowOverlayListeners: Array<(event: FakeDropEvent) => void> = []
  const willDropListeners: Array<(event: FakeDropEvent) => void> = []
  const collectDropListener = <T>(bucket: T[]) => (listener: T) => {
    bucket.push(listener)
    return {
      dispose: () => {
        const idx = bucket.indexOf(listener)
        if (idx >= 0) bucket.splice(idx, 1)
      },
    }
  }
  // Mirror dockview v7's real onDidActivePanelChange payload: `{ panel, origin }`.
  // The old fake used `{ activePanel }` (the v6 shape); that masked the v6→v7
  // rename bug because the store read the same wrong field the fake emitted.
  const activePanelListeners: Array<(event: { panel: FakePanel | undefined }) => void> = []
  const layoutListeners: Array<() => void> = []
  const closeVetoPanelIds = new Set<string>()
  let activePanel: FakePanel | null = null
  let nextGroupNumber = 1
  const setActivePanel = (panel: FakePanel | null) => {
    if (panel?.group) groupActivePanels.set(panel.group.id, panel)
    if (activePanel === panel) return
    activePanel = panel
    activePanelListeners.forEach(listener => listener({ panel: panel ?? undefined }))
  }
  const noopDisposable = () => ({ dispose: () => {} })
  const layoutDisposable = (listener: () => void) => {
    layoutListeners.push(listener)
    return {
      dispose: () => {
        const idx = layoutListeners.indexOf(listener)
        if (idx >= 0) layoutListeners.splice(idx, 1)
      },
    }
  }
  const emitLayoutChange = () => {
    queueMicrotask(() => {
      layoutListeners.forEach(listener => listener())
    })
  }
  const activePanelDisposable = (listener: (event: { panel: FakePanel | undefined }) => void) => {
    activePanelListeners.push(listener)
    return {
      dispose: () => {
        const idx = activePanelListeners.indexOf(listener)
        if (idx >= 0) activePanelListeners.splice(idx, 1)
      },
    }
  }
  const removeDisposable = (listener: (panel: FakePanel) => void) => {
    removeListeners.push(listener)
    return {
      dispose: () => {
        const idx = removeListeners.indexOf(listener)
        if (idx >= 0) removeListeners.splice(idx, 1)
      },
    }
  }
  const createGroup = (id = `group-${++nextGroupNumber}`): FakeGroup => {
    const groupElement = {
      classList: {
        toggle: vi.fn(),
      },
    } as unknown as HTMLElement
    const group: FakeGroup = {
      id,
      element: groupElement,
      model: { header: { hidden: false } },
      api: {
        getHeaderPosition: () => 'top' as const,
        setHeaderPosition: vi.fn(),
        setActivePanel: (panel: FakePanel) => setActivePanel(panel),
        setActive: () => setActivePanel(group.activePanel),
        setSize: vi.fn(),
        // A 'center' group move merges every panel into the target as tabs and
        // dissolves the emptied source — the semantics the mobile breakpoint
        // watcher relies on to collapse a multi-group desktop arrangement.
        moveTo: (options: { group?: FakeGroup, position?: 'top' | 'bottom' | 'left' | 'right' | 'center' }) => {
          const target = options.group ?? primaryGroup
          for (const panel of panels.filter(p => p.group === group)) {
            panel.group = target
            groupActivePanels.set(target.id, panel)
          }
          if (group !== primaryGroup && group.panels.length === 0) {
            const groupIndex = groups.indexOf(group)
            if (groupIndex >= 0) groups.splice(groupIndex, 1)
            groupActivePanels.delete(group.id)
            groupPositions.delete(group.id)
          }
          emitLayoutChange()
        },
      },
      get panels() {
        return panels.filter(panel => panel.group === group)
      },
      get activePanel() {
        return groupActivePanels.get(group.id) ?? group.panels[0] ?? null
      },
    }
    groups.push(group)
    return group
  }
  const primaryGroup = createGroup('group-1')

  const dock = {
    panels,
    groups,
    get activePanel() {
      return activePanel
    },
    get activeGroup() {
      return activePanel?.group ?? primaryGroup
    },
    onDidActivePanelChange: activePanelDisposable,
    onDidLayoutChange: layoutDisposable,
    onDidRemovePanel: removeDisposable,
    onDidAddPanel: noopDisposable,
    onDidMovePanel: noopDisposable,
    onWillShowOverlay: collectDropListener(willShowOverlayListeners),
    onWillDrop: collectDropListener(willDropListeners),
    onWillDragPanel: noopDisposable,
    onWillDragGroup: noopDisposable,
    getGroup(id: string) {
      return groups.find(group => group.id === id)
    },
    getPanel(id: string) {
      return panels.find((p) => p.id === id)
    },
    adjacentGroupInDirection(group: FakeGroup, direction: 'left' | 'right' | 'above' | 'below') {
      return groups.find((candidate) => {
        const position = groupPositions.get(candidate.id)
        return position?.referenceGroup === group.id && position.direction === direction
      })
    },
    addPanel(options: {
      id: string
      component: string
      title?: string
      params?: Record<string, unknown>
      renderer?: string
      inactive?: boolean
      position?: { referenceGroup: string; direction: string }
    }) {
      const referenceGroup = options.position
        ? groups.find(group => group.id === options.position?.referenceGroup)
        : undefined
      const targetGroup = options.position?.direction === 'within'
        ? referenceGroup ?? activePanel?.group ?? primaryGroup
        : options.position
          ? createGroup()
          : activePanel?.group ?? primaryGroup
      if (options.position && options.position.direction !== 'within') {
        groupPositions.set(targetGroup.id, options.position)
      }
      const panel: FakePanel = {
        id: options.id,
        component: options.component,
        params: { ...(options.params ?? {}) },
        title: options.title ?? '',
        renderer: options.renderer,
        api: {
          setActive: () => setActivePanel(panel),
          close: () => {
            if (closeVetoPanelIds.has(panel.id)) return
            const ownerGroup = panel.group
            const idx = panels.indexOf(panel)
            if (idx >= 0) panels.splice(idx, 1)
            const replacement = ownerGroup?.panels[0] ?? null
            if (ownerGroup && groupActivePanels.get(ownerGroup.id) === panel) {
              groupActivePanels.set(ownerGroup.id, replacement)
            }
            if (ownerGroup && ownerGroup !== primaryGroup && ownerGroup.panels.length === 0) {
              const groupIndex = groups.indexOf(ownerGroup)
              if (groupIndex >= 0) groups.splice(groupIndex, 1)
              groupActivePanels.delete(ownerGroup.id)
            }
            removeListeners.forEach(listener => listener(panel))
            if (activePanel === panel) setActivePanel(replacement ?? panels[0] ?? null)
            emitLayoutChange()
          },
          setTitle: (title: string) => {
            panel.title = title
          },
          updateParameters: (params: Record<string, unknown>) => {
            Object.assign(panel.params, params)
          },
          moveTo: (options: { group?: FakeGroup; position?: 'top' | 'bottom' | 'left' | 'right' | 'center' }) => {
            const sourceGroup = panel.group
            const targetGroup = options.position === 'center'
              ? options.group ?? sourceGroup
              : createGroup()
            if (!targetGroup) return
            if (options.position && options.position !== 'center' && options.group) {
              groupPositions.set(targetGroup.id, {
                referenceGroup: options.group.id,
                direction: options.position,
              })
            }
            panel.group = targetGroup
            groupActivePanels.set(targetGroup.id, panel)
            if (sourceGroup && sourceGroup !== primaryGroup && sourceGroup.panels.length === 0) {
              const groupIndex = groups.indexOf(sourceGroup)
              if (groupIndex >= 0) groups.splice(groupIndex, 1)
              groupActivePanels.delete(sourceGroup.id)
              groupPositions.delete(sourceGroup.id)
            }
            setActivePanel(panel)
            emitLayoutChange()
          },
          get title() {
            return panel.title
          },
        },
      }
      panel.group = targetGroup
      panels.push(panel)
      if (!options.inactive) setActivePanel(panel)
      emitLayoutChange()
      return panel
    },
    clear() {
      panels.splice(0, panels.length)
      groups.splice(1, groups.length - 1)
      groupActivePanels.clear()
      groupPositions.clear()
      setActivePanel(null)
      emitLayoutChange()
    },
    vetoCloseUntilAllowed(id: string) {
      closeVetoPanelIds.add(id)
    },
    allowClose(id: string) {
      closeVetoPanelIds.delete(id)
    },
    emitWillShowOverlay(event: FakeDropEvent) {
      willShowOverlayListeners.forEach(listener => listener(event))
    },
    emitWillDrop(event: FakeDropEvent) {
      willDropListeners.forEach(listener => listener(event))
    },
    activePanelListenerCount() {
      return activePanelListeners.length
    },
    toJSON() {
      return {
        grid: { root: { type: 'leaf', data: {} } },
        panels: Object.fromEntries(panels.map(panel => [
          panel.id,
          {
            id: panel.id,
            contentComponent: panel.component,
            title: panel.title,
            params: { ...panel.params },
          },
        ])),
      }
    },
    fromJSON(data?: {
      grid?: {
        root?: {
          type: 'leaf' | 'branch'
          data: unknown
        }
      }
      panels?: Record<string, {
        id?: string
        contentComponent?: string
        title?: string
        params?: Record<string, unknown>
      }>
      activeGroup?: string
    }) {
      dock.clear()
      type SerializedNode = {
        type: 'leaf' | 'branch'
        data: SerializedNode[] | { id?: string; views?: string[]; activeView?: string }
      }
      const leaves: Array<{ id?: string; views?: string[]; activeView?: string }> = []
      const visit = (node?: SerializedNode) => {
        if (!node) return
        if (node.type === 'branch') {
          for (const child of node.data as SerializedNode[]) visit(child)
          return
        }
        leaves.push(node.data as { id?: string; views?: string[]; activeView?: string })
      }
      visit(data?.grid?.root as SerializedNode | undefined)

      if (leaves.length === 0) {
        leaves.push({ id: primaryGroup.id, views: Object.keys(data?.panels ?? {}) })
      }
      const serializedGroups = new Map<string, FakeGroup>()
      leaves.forEach((leaf, index) => {
        const group = index === 0 ? primaryGroup : createGroup()
        if (leaf.id) serializedGroups.set(leaf.id, group)
        for (const id of leaf.views ?? []) {
          const panel = data?.panels?.[id]
          if (!panel) continue
          dock.addPanel({
            id: panel.id ?? id,
            component: panel.contentComponent ?? 'unknown',
            title: panel.title,
            params: panel.params,
            inactive: true,
            position: { referenceGroup: group.id, direction: 'within' },
          })
        }
        const active = group.panels.find(panel => panel.id === leaf.activeView) ?? group.panels[0] ?? null
        groupActivePanels.set(group.id, active)
      })
      const activeGroup = data?.activeGroup ? serializedGroups.get(data.activeGroup) : primaryGroup
      setActivePanel(activeGroup?.activePanel ?? panels[0] ?? null)
      for (const [id, panel] of Object.entries(data?.panels ?? {})) {
        if (dock.getPanel(id)) continue
        dock.addPanel({
          id: panel.id ?? id,
          component: panel.contentComponent ?? 'unknown',
          title: panel.title,
          params: panel.params,
        })
      }
    },
  }
  return dock
}

async function flushDraftChatFallback() {
  await nextTick()
  await new Promise(resolve => setTimeout(resolve, 0))
}

function persistedLayoutWithPanel(
  id: string,
  component: string,
  params: Record<string, unknown>,
  title = 'New Session',
) {
  return {
    panels: {
      [id]: {
        id,
        contentComponent: component,
        title,
        params,
      },
    },
  }
}

describe('workspace layout store', () => {
  afterEach(() => {
    useWorkspaceTabsStore().releaseApi()
  })

  beforeEach(() => {
    localStorage.clear()
    window.localStorage.clear()
    sessionStorage.clear()
    window.sessionStorage.clear()
    chatStoreMock.createNewSession.mockClear()
    chatStoreMock.focusChatView.mockClear()
    chatStoreMock.selectSession.mockClear()
    chatStoreMock.selectDraft.mockClear()
    chatStoreMock.resetToEmptyComposer.mockClear()
    chatStoreMock.applyDraftViewRequest.mockClear()
    chatStoreMock.sessionId = null
    chatStoreMock.hasExplicitSessionSelection = false
    chatStoreMock.loadingChats = false
    chatStoreMock.deletedSession = ref(null)
    chatStoreMock.pendingExternalAgentSessionInput = ref(null)
    chatStoreMock.draftViewRequested = ref(null)
    chatStoreMock.forkedSessionRequested = ref(null)
    chatStoreMock.userSentInSession = ref(null)
    chatStoreMock.guiToolUseRequested = ref(null)
    chatStoreMock.sessions = reactive([]) as typeof chatStoreMock.sessions
    chatStoreMock.knownSessions = reactive([]) as typeof chatStoreMock.knownSessions
    chatStoreMock.knownSessionSummary.mockImplementation((sessionId: string) =>
      chatStoreMock.knownSessions.find(session => session.id === sessionId)
      ?? chatStoreMock.sessions.find(session => session.id === sessionId)
      ?? null,
    )
    setActivePinia(createPinia())
    useChatSelectionStore().setBot('bot-1')
    chatStoreMock.selectSession.mockImplementation(async (sessionId: string, options?: { explicitSelection?: boolean }) => {
      useChatSelectionStore().setSession(sessionId, options)
      chatStoreMock.sessionId = sessionId
      chatStoreMock.hasExplicitSessionSelection = options?.explicitSelection !== false
    })
    chatStoreMock.selectDraft.mockImplementation((options?: { explicitSelection?: boolean }) => {
      useChatSelectionStore().setSession(null, options)
      chatStoreMock.sessionId = null
      chatStoreMock.hasExplicitSessionSelection = options?.explicitSelection === true
    })
    chatStoreMock.resetToEmptyComposer.mockImplementation((options?: {
      clearPendingExternalAgent?: boolean
      explicitSelection?: boolean
      draftIntent?: boolean
    }) => {
      useChatSelectionStore().setSession(null, { explicitSelection: options?.explicitSelection === true })
      chatStoreMock.sessionId = null
      chatStoreMock.hasExplicitSessionSelection = options?.explicitSelection === true
    })
  })

  function emitDeletedSession(id: string, botId = 'bot-1', composerScope?: string) {
    const prevSeq = chatStoreMock.deletedSession.value?.seq ?? 0
    chatStoreMock.deletedSession.value = { id, botId, seq: prevSeq + 1, composerScope }
  }

  function emitUserSentInSession(
    id: string,
    viewId: string,
    wasDraft: boolean,
    botId = 'bot-1',
  ) {
    const prevSeq = chatStoreMock.userSentInSession.value?.seq ?? 0
    chatStoreMock.userSentInSession.value = {
      id,
      botId,
      viewId,
      wasDraft,
      seq: prevSeq + 1,
    }
  }

  function emitGuiToolUse(toolName = 'browser_action') {
    const prevSeq = chatStoreMock.guiToolUseRequested.value?.seq ?? 0
    chatStoreMock.guiToolUseRequested.value = {
      botId: 'bot-1',
      sessionId: 'session-1',
      toolCallId: `call-${prevSeq + 1}`,
      toolName,
      seq: prevSeq + 1,
    }
  }

  it('opens browser panels and updates their address', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openBrowser()

    const panel = dock.getPanel('browser:1')
    expect(panel).toBeTruthy()
    expect(panel?.component).toBe('browser')
    expect(panel?.params.address).toBe('localhost:5173/')

    store.updateBrowserAddress('browser:1', 'localhost:3000/app')
    expect(panel?.params.address).toBe('localhost:3000/app')
    expect(panel?.title).toBe('localhost:3000/app')
  })

  it('opens a browser tab at an address and focuses the existing one on the same URL', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    // First click normalizes the address and titles the tab with it.
    expect(store.openBrowserAt('http://localhost:5173/app')).toBe(true)
    const first = dock.getPanel('browser:1')
    expect(first?.params.address).toBe('localhost:5173/app')
    expect(first?.title).toBe('localhost:5173/app')

    // A different path on the same port opens a second tab.
    expect(store.openBrowserAt('127.0.0.1:5173/other')).toBe(true)
    expect(dock.getPanel('browser:2')?.params.address).toBe('localhost:5173/other')

    // The exact same URL (after normalization) focuses the existing tab.
    expect(store.openBrowserAt('localhost:5173/app')).toBe(true)
    expect(dock.panels.filter(p => p.component === 'browser')).toHaveLength(2)
    expect(dock.activePanel?.id).toBe('browser:1')
  })

  it('opens Desktop in a new right region when a GUI tool starts from a single region', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    store.openDraftChat({ title: 'Session' })

    emitGuiToolUse()

    const display = dock.getPanel('display:1')
    expect(display?.component).toBe('display')
    expect(display?.group?.id).not.toBe('group-1')
    expect(dock.activePanel?.id).toBe('display:1')
  })

  it('opens Desktop as a tab in the existing right region when a GUI tool starts', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    store.openDraftChat({ title: 'Session' })
    store.splitGroup('group-1', 'right')
    const rightGroup = dock.activePanel!.group!

    emitGuiToolUse('computer_observe')

    expect(dock.getPanel('display:1')?.group?.id).toBe(rightGroup.id)
    expect(dock.groups).toHaveLength(2)
  })

  it('creates a right Desktop region when the existing second region is below', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    store.openDraftChat({ title: 'Session' })
    store.splitGroup('group-1', 'below')
    const belowGroup = dock.activePanel!.group!

    emitGuiToolUse()

    const display = dock.getPanel('display:1')!
    expect(display.group?.id).not.toBe(belowGroup.id)
    expect(dock.groups).toHaveLength(3)
  })

  it('focuses the existing Desktop tab in the right region when a GUI tool starts again', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    store.openDraftChat({ title: 'Session' })
    store.splitGroup('group-1', 'right')
    const rightChat = dock.activePanel!
    emitGuiToolUse()
    const display = dock.getPanel('display:1')!
    rightChat.api.setActive()
    expect(dock.activePanel?.id).toBe(rightChat.id)

    emitGuiToolUse('computer_action')

    expect(dock.activePanel?.id).toBe(display.id)
    expect(display.group?.id).toBe(rightChat.group?.id)
  })

  it('refuses to open a browser tab for a non-local address', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    expect(store.openBrowserAt('https://example.com/')).toBe(false)
    expect(dock.panels.filter(p => p.component === 'browser')).toHaveLength(0)
  })

  it('keeps terminal ids monotonic per bot', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openTerminal()
    expect(dock.getPanel('terminal:1')?.title).toBe('Terminal 1')
    store.openTerminal()
    expect(dock.getPanel('terminal:2')?.title).toBe('Terminal 2')
    store.closeTab('terminal:1')
    store.openTerminal()

    expect(dock.getPanel('terminal:1')).toBeUndefined()
    expect(dock.getPanel('terminal:2')).toBeTruthy()
    expect(dock.getPanel('terminal:3')).toBeTruthy()
  })

  it('repairs legacy terminal titles when restoring a layout', async () => {
    localStorage.setItem('workspace-layout', JSON.stringify({
      'bot-1': {
        layout: {
          panels: {
            'terminal:1': {
              id: 'terminal:1',
              contentComponent: 'terminal',
              title: 'zsh',
            },
            'terminal:2': {
              id: 'terminal:2',
              contentComponent: 'terminal',
              title: '',
            },
          },
        },
        terminalCounter: 2,
        ephemeralIds: [],
      },
    }))

    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    await nextTick()

    expect(dock.getPanel('terminal:1')?.title).toBe('Terminal 1')
    expect(dock.getPanel('terminal:2')?.title).toBe('Terminal 2')
  })

  it('updates only the intended terminal title and ignores invalid or repeated events', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    store.openTerminal()
    store.openTerminal()

    const first = dock.getPanel('terminal:1')!
    const second = dock.getPanel('terminal:2')!
    const setTitle = vi.spyOn(first.api, 'setTitle')

    store.updateTerminalTitle(first.id, '  python  ')

    expect(first.title).toBe('python')
    expect(second.title).toBe('Terminal 2')
    expect(setTitle).toHaveBeenCalledTimes(1)

    store.updateTerminalTitle(first.id, 'python')
    store.updateTerminalTitle(first.id, '   ')
    store.updateTerminalTitle(first.id, null)

    expect(first.title).toBe('python')
    expect(setTitle).toHaveBeenCalledTimes(1)
  })

  it('starts a split terminal with its own fallback title', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    store.openTerminal()

    const first = dock.getPanel('terminal:1')!
    store.updateTerminalTitle(first.id, 'node')
    store.splitGroup(first.group!.id, 'right')

    expect(first.title).toBe('node')
    expect(dock.getPanel('terminal:2')?.title).toBe('Terminal 2')
  })

  it('duplicates the active file into a split pane with a unique panel id', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openFile('/data/notes/todo.md')
    store.splitGroup('group-1', 'right')

    expect(dock.getPanel('file:/data/notes/todo.md')).toBeTruthy()
    expect(dock.getPanel('file:/data/notes/todo.md~2')).toBeTruthy()
    expect(dock.panels).toHaveLength(2)
  })

  it('focuses the single draft chat tab instead of duplicating it', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openDraftChat({ title: 'First' })
    store.openTerminal()
    store.openDraftChat({ title: 'Second' })

    expect(dock.panels.filter((p) => p.component === 'chat')).toHaveLength(1)
    expect(dock.activePanel?.component).toBe('chat')
  })

  it('keeps drafts group-scoped and promotes only the view that sent', async () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openDraftChat({ title: 'Left draft', groupId: 'group-1' })
    const leftDraft = dock.activePanel!
    store.splitGroup('group-1', 'right')
    const rightDraft = dock.activePanel!

    expect(rightDraft.id).not.toBe(leftDraft.id)
    expect(rightDraft.group?.id).not.toBe(leftDraft.group?.id)
    expect(dock.panels.filter(panel => panel.component === 'chat')).toHaveLength(2)

    // An explicit group focuses that group's own draft, even while another
    // draft exists elsewhere in the dock.
    store.openDraftChat({ groupId: leftDraft.group!.id })
    expect(dock.activePanel?.id).toBe(leftDraft.id)

    emitUserSentInSession('ignored-session', rightDraft.id, true, 'other-bot')
    await nextTick()
    expect(leftDraft.params.sessionId ?? null).toBeNull()
    expect(rightDraft.params.sessionId ?? null).toBeNull()

    // The signal points at the inactive right view. Promotion must not guess the
    // active (left) draft.
    emitUserSentInSession('right-session', rightDraft.id, true)
    await nextTick()

    expect(leftDraft.params.sessionId ?? null).toBeNull()
    expect(rightDraft.params.sessionId).toBe('right-session')
    expect(store.ephemeralPanels[leftDraft.id]).toBe(true)
    expect(store.ephemeralPanels[rightDraft.id]).toBeUndefined()
  })

  it('keeps a non-explicit draft non-explicit after an explicit session was active', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openSessionChat({ sessionId: 's1', title: 'Session 1', explicitSelection: true })
    expect(chatStoreMock.hasExplicitSessionSelection).toBe(true)

    store.openDraftChat({ title: 'New Session', explicitSelection: false })

    expect(dock.activePanel?.params).toMatchObject({ sessionId: null, explicitSelection: false })
    expect(chatStoreMock.selectDraft).toHaveBeenLastCalledWith({ explicitSelection: false })
    expect(chatStoreMock.hasExplicitSessionSelection).toBe(false)
  })

  it('opens an explicit draft from a stale active chat session', async () => {
    const selection = useChatSelectionStore()
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openSessionChat({ sessionId: 's1', title: 'Session 1' })
    expect(dock.activePanel?.params.sessionId).toBe('s1')

    selection.setSession('s1', { explicitSelection: true })
    await nextTick()
    chatStoreMock.sessionId = null
    chatStoreMock.hasExplicitSessionSelection = true
    selection.setSession(null, { explicitSelection: true })
    await nextTick()

    expect(dock.activePanel?.component).toBe('chat')
    expect(dock.activePanel?.params).toMatchObject({ sessionId: null, explicitSelection: true })
    expect(chatStoreMock.selectDraft).toHaveBeenLastCalledWith({ explicitSelection: true })
  })

  it('does not let restored draft panel params clear an explicit empty composer', async () => {
    const staleLayout = persistedLayoutWithPanel(
      'chat:bot-1:1',
      'chat',
      { sessionId: null, explicitSelection: false },
    )
    localStorage.setItem('workspace-layout', JSON.stringify({
      'bot-1': {
        layout: staleLayout,
        ephemeralIds: ['chat:bot-1:1'],
      },
    }))
    chatStoreMock.hasExplicitSessionSelection = true
    useChatSelectionStore().setSession(null, { explicitSelection: true })

    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    await nextTick()

    expect(chatStoreMock.selectDraft).toHaveBeenLastCalledWith({ explicitSelection: true })
    expect(chatStoreMock.hasExplicitSessionSelection).toBe(true)
  })

  it('does not let a restored draft chat tab clear an explicitly selected session', async () => {
    const staleLayout = persistedLayoutWithPanel(
      'chat:bot-1:1',
      'chat',
      { sessionId: null, explicitSelection: false },
    )
    localStorage.setItem('workspace-layout', JSON.stringify({
      'bot-1': {
        layout: staleLayout,
        ephemeralIds: ['chat:bot-1:1'],
      },
    }))
    chatStoreMock.sessionId = 'history-session-1'
    chatStoreMock.hasExplicitSessionSelection = true
    useChatSelectionStore().setSession('history-session-1')

    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    await nextTick()

    expect(chatStoreMock.selectDraft).not.toHaveBeenCalled()
    expect(chatStoreMock.selectSession).toHaveBeenCalledWith('history-session-1')
    expect(dock.activePanel?.params.sessionId).toBe('history-session-1')
  })

  it('keeps a restored session chat without adding a draft when selection is empty', async () => {
    const restoredLayout = persistedLayoutWithPanel(
      'chat:bot-1:1',
      'chat',
      { sessionId: 'session-1', explicitSelection: true },
      'Session 1',
    )
    localStorage.setItem('workspace-layout', JSON.stringify({
      'bot-1': {
        layout: restoredLayout,
        ephemeralIds: [],
      },
    }))

    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    await nextTick()

    expect(dock.panels).toHaveLength(1)
    expect(dock.activePanel?.params.sessionId).toBe('session-1')
    expect(dock.panels.some(panel => panel.component === 'chat' && panel.params.sessionId == null)).toBe(false)
    expect(chatStoreMock.focusChatView).toHaveBeenCalledWith('chat:bot-1:1')
    // Recents follows chat-session-id — pull it onto the restored panel.
    expect(chatStoreMock.selectSession).toHaveBeenCalledWith('session-1', { explicitSelection: false })
    expect(useChatSelectionStore().sessionId).toBe('session-1')
  })

  it('reconciles an auto-picked session to the restored chat panel without opening another tab', async () => {
    const restoredLayout = persistedLayoutWithPanel(
      'chat:bot-1:1',
      'chat',
      { sessionId: 'restored-session', explicitSelection: true },
      'Restored',
    )
    localStorage.setItem('workspace-layout', JSON.stringify({
      'bot-1': {
        layout: restoredLayout,
        ephemeralIds: [],
      },
    }))

    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    await nextTick()
    chatStoreMock.selectSession.mockClear()

    // initialize() auto-picks a newer Untitled while the dock still shows the
    // restored conversation — selection must snap back to the open panel.
    chatStoreMock.sessionId = 'untitled-latest'
    chatStoreMock.hasExplicitSessionSelection = false
    chatStoreMock.loadingChats = false
    chatStoreMock.sessions.push(
      { id: 'untitled-latest', title: '' },
      { id: 'restored-session', title: 'Restored' },
    )
    useChatSelectionStore().setSession('untitled-latest', { explicitSelection: false })
    await nextTick()
    await flushDraftChatFallback()

    expect(dock.panels).toHaveLength(1)
    expect(dock.activePanel?.params.sessionId).toBe('restored-session')
    expect(chatStoreMock.selectSession).toHaveBeenCalledWith('restored-session', { explicitSelection: false })
    expect(useChatSelectionStore().sessionId).toBe('restored-session')
    expect(chatStoreMock.hasExplicitSessionSelection).toBe(false)
  })

  it('does not open a chat tab when initialize auto-picks a session over a restored file/preview workspace', async () => {
    // Cold-start seed: File + Preview only (the user's Tab A layout). A new
    // browser tab must restore that dock as-is — initialize()'s non-explicit
    // auto-pick of the latest Untitled Session must NOT punch chat tabs in,
    // and must not leave Recents highlighting a session that is not on screen.
    localStorage.setItem('workspace-layout', JSON.stringify({
      'bot-1': {
        layout: {
          panels: {
            'file:/data/AGENTS.md': {
              id: 'file:/data/AGENTS.md',
              contentComponent: 'file',
              title: 'AGENTS.md',
              params: { filePath: '/data/AGENTS.md' },
            },
            'preview:/data/AGENTS.md': {
              id: 'preview:/data/AGENTS.md',
              contentComponent: 'preview',
              title: 'Preview: AGENTS.md',
              params: { filePath: '/data/AGENTS.md' },
            },
          },
        },
        ephemeralIds: ['file:/data/AGENTS.md', 'preview:/data/AGENTS.md'],
      },
    }))

    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    await nextTick()

    expect(dock.panels.map(panel => panel.component).sort()).toEqual(['file', 'preview'])
    expect(dock.panels.some(panel => panel.component === 'chat')).toBe(false)

    // Simulate initialize() finishing: it writes the latest history item into
    // selection with explicitSelection=false (see bootstrap.ts).
    chatStoreMock.sessionId = 'untitled-1'
    chatStoreMock.hasExplicitSessionSelection = false
    chatStoreMock.loadingChats = false
    chatStoreMock.sessions.push({ id: 'untitled-1', title: '' })
    useChatSelectionStore().setSession('untitled-1', { explicitSelection: false })
    await nextTick()
    await flushDraftChatFallback()

    expect(dock.panels.map(panel => panel.component).sort()).toEqual(['file', 'preview'])
    expect(dock.panels.some(panel => panel.component === 'chat')).toBe(false)
    expect(chatStoreMock.resetToEmptyComposer).toHaveBeenCalledWith({
      clearPendingExternalAgent: false,
      explicitSelection: false,
      draftIntent: false,
    })
    expect(chatStoreMock.sessionId).toBeNull()
    expect(useChatSelectionStore().sessionId).toBeNull()
    expect(chatStoreMock.selectDraft).not.toHaveBeenCalled()
  })

  it('still opens a chat tab on explicit session selection after a file/preview restore', async () => {
    localStorage.setItem('workspace-layout', JSON.stringify({
      'bot-1': {
        layout: {
          panels: {
            'file:/data/AGENTS.md': {
              id: 'file:/data/AGENTS.md',
              contentComponent: 'file',
              title: 'AGENTS.md',
              params: { filePath: '/data/AGENTS.md' },
            },
          },
        },
        ephemeralIds: [],
      },
    }))

    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    await nextTick()

    chatStoreMock.sessionId = 'picked-1'
    chatStoreMock.hasExplicitSessionSelection = true
    chatStoreMock.sessions.push({ id: 'picked-1', title: 'Picked' })
    useChatSelectionStore().setSession('picked-1', { explicitSelection: true })
    await nextTick()

    expect(dock.panels.some(panel => panel.component === 'chat' && panel.params.sessionId === 'picked-1')).toBe(true)
  })

  it('does not promote a restored auto-selected native session into explicit selection while chats load', async () => {
    const staleLayout = persistedLayoutWithPanel(
      'chat:bot-1:1',
      'chat',
      { sessionId: 'native-session' },
      'Native Session',
    )
    localStorage.setItem('workspace-layout', JSON.stringify({
      'bot-1': {
        layout: staleLayout,
        ephemeralIds: ['chat:bot-1:1'],
      },
    }))
    chatStoreMock.sessionId = 'native-session'
    chatStoreMock.hasExplicitSessionSelection = false
    chatStoreMock.loadingChats = true
    useChatSelectionStore().setSession('native-session', { explicitSelection: false })

    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    await nextTick()

    expect(chatStoreMock.selectSession).not.toHaveBeenCalled()
    expect(chatStoreMock.hasExplicitSessionSelection).toBe(false)
    expect(dock.activePanel?.params.sessionId).toBe('native-session')
  })

  it('opens one chat tab per session and reuses the ephemeral slot', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openSessionChat({ sessionId: 's1', title: 'S1' })
    const firstId = dock.panels.find((p) => p.component === 'chat')!.id

    // Same session: focus the existing tab, no new panel.
    chatStoreMock.selectSession.mockClear()
    store.openSessionChat({ sessionId: 's1', title: 'S1' })
    expect(dock.panels.filter((p) => p.component === 'chat')).toHaveLength(1)
    expect(chatStoreMock.selectSession).toHaveBeenCalledWith('s1')

    // Different session: repoint the group's ephemeral chat slot in place.
    store.openSessionChat({ sessionId: 's2', title: 'S2' })
    const chatPanels = dock.panels.filter((p) => p.component === 'chat')
    expect(chatPanels).toHaveLength(1)
    expect(chatPanels[0]!.id).toBe(firstId)
    expect(chatPanels[0]!.params.sessionId).toBe('s2')
  })

  it('reuses an ephemeral chat tab even when its session is off the current page', () => {
    chatStoreMock.sessions.push({ id: 's2', title: 'S2' })
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openSessionChat({ sessionId: 'older-session', title: 'Older' })
    const firstId = dock.panels.find((p) => p.component === 'chat')!.id

    store.openSessionChat({ sessionId: 's2', title: 'S2' })

    const chatPanels = dock.panels.filter((p) => p.component === 'chat')
    expect(chatPanels).toHaveLength(1)
    expect(chatPanels[0]!.id).toBe(firstId)
    expect(chatPanels[0]!.params.sessionId).toBe('s2')
  })

  it('uses remembered hidden session summaries for subagent chat tabs', () => {
    chatStoreMock.knownSessionSummary.mockImplementation((sessionId: string) => {
      if (sessionId === 'subagent-1') {
        return {
          id: 'subagent-1',
          title: 'Subagent task',
          type: 'subagent',
        }
      }
      return chatStoreMock.sessions.find(session => session.id === sessionId) ?? null
    })
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openSessionChat({ sessionId: 'subagent-1' })

    const chatPanel = dock.panels.find(panel => panel.component === 'chat')
    expect(chatPanel?.params.sessionId).toBe('subagent-1')
    expect(chatPanel?.title).toBe('Subagent task')
  })

  it('updates open hidden subagent tab titles when their summary is remembered later', async () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openSessionChat({ sessionId: 'subagent-1', title: 'agent-a' })
    const chatPanel = dock.panels.find(panel => panel.component === 'chat')!
    expect(chatPanel.title).toBe('agent-a')

    chatStoreMock.knownSessions.push({
      id: 'subagent-1',
      title: 'Fetched subagent task',
      type: 'subagent',
    })
    await nextTick()

    expect(dock.getPanel(chatPanel.id)?.title).toBe('Fetched subagent task')
  })

  it('opens the first subagent session in a right split and later ones as tabs beside it', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    // Anchor the primary region with the conversation tab.
    store.openSessionChat({ sessionId: 'parent-1', title: 'Parent' })
    const parentPanel = dock.panels.find(panel => panel.component === 'chat')!
    const primaryGroupId = parentPanel.group.id

    store.openSubagentSession({ sessionId: 'subagent-1', title: 'agent-a' })
    const first = dock.panels.find(panel => panel.params.sessionId === 'subagent-1')!
    expect(first.group.id).not.toBe(primaryGroupId)

    store.openSubagentSession({ sessionId: 'subagent-2', title: 'agent-b' })
    const second = dock.panels.find(panel => panel.params.sessionId === 'subagent-2')!
    // The region already exists: the second subagent joins it as a tab
    // instead of expanding again.
    expect(second.group.id).toBe(first.group.id)
    expect(dock.groups.filter(group => group.id !== primaryGroupId)).toHaveLength(1)
  })

  it('focuses an already-open subagent tab instead of opening a duplicate', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openSessionChat({ sessionId: 'parent-1', title: 'Parent' })
    store.openSubagentSession({ sessionId: 'subagent-1', title: 'agent-a' })
    const panelCount = dock.panels.length

    store.openSubagentSession({ sessionId: 'subagent-1', title: 'agent-a' })

    expect(dock.panels.length).toBe(panelCount)
    expect(dock.activePanel?.params.sessionId).toBe('subagent-1')
  })

  it('does not reconcile remembered hidden subagent chat tabs as deleted', async () => {
    chatStoreMock.knownSessionSummary.mockImplementation((sessionId: string) => {
      if (sessionId === 'subagent-1') {
        return {
          id: 'subagent-1',
          title: 'Subagent task',
          type: 'subagent',
        }
      }
      return chatStoreMock.sessions.find(session => session.id === sessionId) ?? null
    })
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    store.openSessionChat({ sessionId: 'subagent-1' })
    const chatPanel = dock.panels.find(panel => panel.component === 'chat')!

    chatStoreMock.sessions.push({ id: 'parent-1', title: 'Parent' })
    await nextTick()

    expect(dock.getPanel(chatPanel.id)?.params.sessionId).toBe('subagent-1')
    expect(chatStoreMock.selectDraft).not.toHaveBeenCalled()
  })

  it('closes the active deleted chat tab and opens the newly selected session', async () => {
    const selection = useChatSelectionStore()
    selection.setSession('s1')
    chatStoreMock.sessions.push(
      { id: 's1', title: 'Deleted session' },
      { id: 's2', title: 'Next session' },
    )
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openSessionChat({ sessionId: 's1', title: 'Deleted session' })
    const chatPanel = dock.panels.find(panel => panel.component === 'chat')!

    emitDeletedSession('s1')
    chatStoreMock.sessions.splice(0, chatStoreMock.sessions.length, { id: 's2', title: 'Next session' })
    selection.setSession('s2')
    await nextTick()

    expect(dock.getPanel(chatPanel.id)).toBeUndefined()
    const nextChatPanel = dock.panels.find(panel => panel.component === 'chat')
    expect(nextChatPanel?.params.sessionId).toBe('s2')
    expect(nextChatPanel?.title).toBe('Next session')
    expect(chatStoreMock.selectDraft).not.toHaveBeenCalled()
  })

  it('resets a failed deferred-session chat panel to draft when its composer scope matches', async () => {
    const selection = useChatSelectionStore()
    selection.setSession('s1')
    chatStoreMock.sessionId = null
    chatStoreMock.sessions.push({ id: 's1', title: 'Created then failed' })
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openSessionChat({ sessionId: 's1', title: 'Created then failed' })
    const panel = dock.panels.find(panel => panel.component === 'chat')!
    store.pinPanel(panel.id)

    selection.setSession(null)
    emitDeletedSession('s1', 'bot-1', `bot-1:${panel.id}`)
    await nextTick()

    expect(dock.getPanel(panel.id)).toBe(panel)
    expect(panel.params.sessionId ?? null).toBeNull()
    expect(panel.title).toBe('New Session')
    expect(store.ephemeralPanels[panel.id]).toBe(true)
    expect(chatStoreMock.selectDraft).toHaveBeenCalled()
  })

  it('does not let deleted-tab auto-activation override the fallback session', async () => {
    const selection = useChatSelectionStore()
    selection.setSession('s1')
    chatStoreMock.sessions.push(
      { id: 's1', title: 'Deleted session' },
      { id: 's2', title: 'Fallback session' },
      { id: 's3', title: 'Neighbor session' },
    )
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openSessionChat({ sessionId: 's1', title: 'Deleted session' })
    const deletedPanel = dock.panels.find(panel => panel.component === 'chat' && panel.params.sessionId === 's1')!
    store.pinPanel(deletedPanel.id)
    store.openSessionChat({ sessionId: 's3', title: 'Neighbor session' })
    deletedPanel.api.setActive()
    expect(selection.sessionId).toBe('s1')
    chatStoreMock.selectSession.mockClear()

    chatStoreMock.sessions.splice(0, chatStoreMock.sessions.length,
      { id: 's2', title: 'Fallback session' },
      { id: 's3', title: 'Neighbor session' },
    )
    emitDeletedSession('s1')
    chatStoreMock.hasExplicitSessionSelection = false
    selection.setSession('s2')
    await nextTick()
    await nextTick()

    expect(dock.getPanel(deletedPanel.id)).toBeUndefined()
    expect(selection.sessionId).toBe('s2')
    expect(chatStoreMock.selectSession).not.toHaveBeenCalledWith('s3')
    expect(dock.activePanel?.params.sessionId).toBe('s2')
    expect(chatStoreMock.hasExplicitSessionSelection).toBe(false)
    expect(chatStoreMock.selectDraft).not.toHaveBeenCalled()
  })

  it('keeps open chat tabs when a paginated session list refresh drops their id', async () => {
    const selection = useChatSelectionStore()
    selection.setSession('s1')
    chatStoreMock.sessions.push(
      { id: 's1', title: 'Open older session' },
      { id: 's2', title: 'Other session' },
    )
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openSessionChat({ sessionId: 's1', title: 'Open older session' })
    const openPanel = dock.panels.find(panel => panel.component === 'chat')!

    chatStoreMock.sessions.splice(0, chatStoreMock.sessions.length,
      { id: 's2', title: 'Other session' },
      { id: 's3', title: 'Replacement page item' },
    )
    await nextTick()

    expect(dock.getPanel(openPanel.id)?.params.sessionId).toBe('s1')
    expect(chatStoreMock.selectDraft).not.toHaveBeenCalled()
  })

  it('buffers deleted-session signals for inactive bots and applies them after switching back', async () => {
    const selection = useChatSelectionStore()
    selection.setSession('s1')
    chatStoreMock.sessions.push({ id: 's1', title: 'Deleted session' })
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openSessionChat({ sessionId: 's1', title: 'Deleted session' })
    const deletedPanel = dock.panels.find(panel => panel.component === 'chat')!

    selection.setBot('bot-without-layout')
    emitDeletedSession('s1', 'bot-1')
    await nextTick()
    expect(dock.getPanel(deletedPanel.id)).toBeUndefined()

    selection.setBot('bot-1')
    await nextTick()
    await nextTick()

    expect(dock.getPanel(deletedPanel.id)).toBeUndefined()
    expect(dock.panels.some(panel => panel.component === 'chat' && panel.params.sessionId === 's1')).toBe(false)
  })

  it('keeps buffered delete reconciliation across dock api re-registration', async () => {
    const selection = useChatSelectionStore()
    selection.setSession('s1')
    chatStoreMock.sessions.push({ id: 's1', title: 'Deleted session' })
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openSessionChat({ sessionId: 's1', title: 'Deleted session' })
    const deletedPanel = dock.panels.find(panel => panel.component === 'chat')!

    selection.setBot('bot-without-layout')
    emitDeletedSession('s1', 'bot-1')
    await nextTick()
    store.releaseApi()
    expect(dock.activePanelListenerCount()).toBe(0)

    const nextDock = createFakeDock()
    store.registerApi(nextDock as never)
    selection.setBot('bot-1')
    await nextTick()
    await nextTick()

    expect(nextDock.getPanel(deletedPanel.id)).toBeUndefined()
    expect(nextDock.panels.some(panel => panel.component === 'chat' && panel.params.sessionId === 's1')).toBe(false)
  })

  it('does not reopen a tombstoned chat session from a stale caller', async () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    emitDeletedSession('s1', 'bot-1')
    await nextTick()

    store.openSessionChat({ sessionId: 's1', title: 'Deleted session' })

    expect(dock.panels.some(panel => panel.component === 'chat' && panel.params.sessionId === 's1')).toBe(false)
    expect(chatStoreMock.selectSession).not.toHaveBeenCalledWith('s1')
  })

  it('opens files into the ephemeral slot and replaces until pinned', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openFile('/data/a.md')
    expect(store.ephemeralPanels['file:/data/a.md']).toBe(true)

    // Another file replaces the ephemeral one in the same group.
    store.openFile('/data/b.md')
    expect(dock.getPanel('file:/data/a.md')).toBeUndefined()
    expect(dock.getPanel('file:/data/b.md')).toBeTruthy()

    // Pin b (mimics a first edit); opening c now keeps b.
    store.pinPanel('file:/data/b.md')
    expect(store.ephemeralPanels['file:/data/b.md']).toBeUndefined()
    store.openFile('/data/c.md')
    expect(dock.getPanel('file:/data/b.md')).toBeTruthy()
    expect(dock.getPanel('file:/data/c.md')).toBeTruthy()
  })

  it('keeps the final draft chat tab open when it is closed', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openDraftChat({ title: 'Existing' })
    const id = dock.panels.find((p) => p.component === 'chat')!.id
    store.closeTab(id)

    expect(dock.panels).toHaveLength(1)
    expect(dock.getPanel(id)?.component).toBe('chat')
  })

  it('respawns a fresh draft when the last chat tab (a real session) is closed', async () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openSessionChat({ sessionId: 's1', title: 'S1' })
    const id = dock.panels.find((p) => p.component === 'chat')!.id

    store.closeTab(id)
    // Closing the last chat resets the global view to a draft so the respawn is a
    // fresh New Session page, not the session that was just closed.
    expect(chatStoreMock.selectDraft).toHaveBeenCalled()

    await flushDraftChatFallback()
    const chatPanels = dock.panels.filter((p) => p.component === 'chat')
    expect(chatPanels).toHaveLength(1)
    expect(chatPanels[0]!.params.sessionId ?? null).toBeNull()
  })

  it('opens a draft chat when a non-chat tab leaves the dock empty', async () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openTerminal()
    store.closeTab('terminal:1')

    await flushDraftChatFallback()

    expect(dock.panels).toHaveLength(1)
    const chat = dock.panels[0]!
    expect(chat.component).toBe('chat')
    expect(chat.params.sessionId ?? null).toBeNull()
  })

  it('opens a draft — not an auto-picked Untitled session — after closing a restored file/preview workspace', async () => {
    // Cold-start tab: layout seeded from localStorage, initialize() wrote a
    // non-explicit sessionId we refused to open. Closing the last file must
    // refill New Session, not that stale Untitled pick.
    localStorage.setItem('workspace-layout', JSON.stringify({
      'bot-1': {
        layout: {
          panels: {
            'file:/data/AGENTS.md': {
              id: 'file:/data/AGENTS.md',
              contentComponent: 'file',
              title: 'AGENTS.md',
              params: { filePath: '/data/AGENTS.md' },
            },
            'preview:/data/AGENTS.md': {
              id: 'preview:/data/AGENTS.md',
              contentComponent: 'preview',
              title: 'Preview: AGENTS.md',
              params: { filePath: '/data/AGENTS.md' },
            },
          },
        },
        ephemeralIds: ['file:/data/AGENTS.md', 'preview:/data/AGENTS.md'],
      },
    }))

    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    await nextTick()

    chatStoreMock.sessionId = 'untitled-stale'
    chatStoreMock.hasExplicitSessionSelection = false
    chatStoreMock.sessions.push({ id: 'untitled-stale', title: '' })
    useChatSelectionStore().setSession('untitled-stale', { explicitSelection: false })
    await nextTick()

    expect(dock.panels.some(panel => panel.component === 'chat')).toBe(false)

    store.closeTab('preview:/data/AGENTS.md')
    store.closeTab('file:/data/AGENTS.md')
    await flushDraftChatFallback()

    expect(dock.panels).toHaveLength(1)
    const chat = dock.panels[0]!
    expect(chat.component).toBe('chat')
    expect(chat.params.sessionId ?? null).toBeNull()
    expect(chat.title).toBe('New Session')
  })

  it('refills an explicitly selected session when the last non-chat tab closes', async () => {
    localStorage.setItem('workspace-layout', JSON.stringify({
      'bot-1': {
        layout: {
          panels: {
            'file:/data/AGENTS.md': {
              id: 'file:/data/AGENTS.md',
              contentComponent: 'file',
              title: 'AGENTS.md',
              params: { filePath: '/data/AGENTS.md' },
            },
          },
        },
        ephemeralIds: [],
      },
    }))

    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    await nextTick()

    // Drive chatStore directly: an already-explicit selection whose chat tab is
    // not open (e.g. replaced by a file). Closing the file should restore it.
    chatStoreMock.sessionId = 'explicit-1'
    chatStoreMock.hasExplicitSessionSelection = true
    chatStoreMock.sessions.push({ id: 'explicit-1', title: 'Kept Session' })
    expect(dock.panels.some(panel => panel.component === 'chat')).toBe(false)

    store.closeTab('file:/data/AGENTS.md')
    await flushDraftChatFallback()

    const chat = dock.panels.find(panel => panel.component === 'chat')
    expect(chat?.params.sessionId).toBe('explicit-1')
    expect(chat?.title).toBe('Kept Session')
  })

  it('opens a draft chat when switching to a bot without a saved layout', async () => {
    const selection = useChatSelectionStore()
    selection.setBot(null)
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    expect(dock.panels).toHaveLength(0)

    selection.setBot('bot-without-layout')
    await flushDraftChatFallback()

    expect(dock.panels).toHaveLength(1)
    const chat = dock.panels[0]!
    expect(chat.component).toBe('chat')
    expect(chat.title).toBe('New Session')
  })

  it('splits a chat session into a second group and reuses the split preview in place', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openSessionChat({
      sessionId: 's1',
      title: 'Session',
      explicitSelection: false,
    })
    const source = dock.panels.find(panel => panel.component === 'chat')!
    store.splitGroup('group-1', 'right')

    const split = dock.panels.find(panel => panel.component === 'chat' && panel.id !== source.id)!
    expect(dock.panels.filter(panel => panel.component === 'chat')).toHaveLength(2)
    expect(split.params).toMatchObject({ sessionId: 's1', explicitSelection: false })
    expect(split.title).toBe('Session')
    expect(split.renderer).toBe('always')
    expect(split.group?.id).not.toBe(source.group?.id)
    expect(store.ephemeralPanels[split.id]).toBe(true)
    expect(chatStoreMock.focusChatView).toHaveBeenLastCalledWith(split.id)

    store.openSessionChat({
      sessionId: 's2',
      title: 'Other session',
      groupId: split.group!.id,
    })

    expect(dock.panels.filter(panel => panel.component === 'chat')).toHaveLength(2)
    expect(dock.getPanel(source.id)?.params.sessionId).toBe('s1')
    expect(dock.getPanel(split.id)?.params.sessionId).toBe('s2')
    expect(dock.getPanel(split.id)?.title).toBe('Other session')
  })

  it('applies a late Draft request only to its originating split', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    store.openSessionChat({ sessionId: 'session-a', title: 'A' })
    const origin = dock.activePanel!
    store.splitGroup(origin.group!.id, 'right')
    const other = dock.activePanel!
    store.openSessionChat({ sessionId: 'session-b', title: 'B', groupId: other.group!.id })
    expect(dock.activePanel?.id).toBe(other.id)

    const request = {
      botId: 'bot-1',
      viewId: origin.id,
      expectedSessionId: 'session-a',
      explicitSelection: true,
      input: { agentId: 'codex' },
      activate: true,
      seq: 1,
    }
    chatStoreMock.draftViewRequested.value = request

    expect(dock.getPanel(origin.id)?.params).toMatchObject({ sessionId: null, explicitSelection: true })
    expect(dock.getPanel(origin.id)?.title).toBe('New Session')
    expect(dock.getPanel(other.id)?.params.sessionId).toBe('session-b')
    expect(dock.activePanel?.id).toBe(other.id)
    expect(chatStoreMock.applyDraftViewRequest).toHaveBeenCalledWith(request, false)
  })

  it('keeps a pinned Session and opens its requested Draft as a new preview', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    store.openSessionChatPinned({ sessionId: 'session-a', title: 'A' })
    const origin = dock.activePanel!

    const request = {
      botId: 'bot-1',
      viewId: origin.id,
      expectedSessionId: 'session-a',
      explicitSelection: true,
      input: { agentId: 'codex' },
      activate: true,
      seq: 1,
    }
    chatStoreMock.draftViewRequested.value = request

    const draft = dock.panels.find(panel => panel.component === 'chat' && panel.params.sessionId == null)!
    expect(dock.panels.filter(panel => panel.component === 'chat')).toHaveLength(2)
    expect(origin.params.sessionId).toBe('session-a')
    expect(store.ephemeralPanels[origin.id]).toBeUndefined()
    expect(draft.group?.id).toBe(origin.group?.id)
    expect(store.ephemeralPanels[draft.id]).toBe(true)
    expect(dock.activePanel?.id).toBe(draft.id)
    expect(chatStoreMock.applyDraftViewRequest).toHaveBeenCalledWith(
      expect.objectContaining({ ...request, viewId: draft.id }),
      true,
    )
  })

  it('does not focus a pinned Session Draft when its request resolves behind another split', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    store.openSessionChatPinned({ sessionId: 'session-a', title: 'A' })
    const origin = dock.activePanel!
    store.splitGroup(origin.group!.id, 'right')
    const other = dock.activePanel!
    store.openSessionChat({ sessionId: 'session-b', title: 'B', groupId: other.group!.id })

    chatStoreMock.draftViewRequested.value = {
      botId: 'bot-1',
      viewId: origin.id,
      expectedSessionId: 'session-a',
      explicitSelection: true,
      input: null,
      activate: true,
      seq: 1,
    }

    const draft = origin.group!.panels.find(panel => panel.params.sessionId == null)!
    expect(origin.params.sessionId).toBe('session-a')
    expect(draft).toBeTruthy()
    expect(store.ephemeralPanels[draft.id]).toBe(true)
    expect(dock.activePanel?.id).toBe(other.id)
    expect(chatStoreMock.applyDraftViewRequest).toHaveBeenCalledWith(
      expect.objectContaining({ viewId: draft.id, expectedSessionId: 'session-a' }),
      false,
    )
  })

  it('routes a late Fork to its originating preview without changing the focused split', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    store.openSessionChat({ sessionId: 'session-a', title: 'A' })
    const origin = dock.activePanel!
    store.splitGroup(origin.group!.id, 'right')
    const other = dock.activePanel!
    store.openSessionChat({ sessionId: 'session-b', title: 'B', groupId: other.group!.id })

    chatStoreMock.forkedSessionRequested.value = {
      botId: 'bot-1',
      viewId: origin.id,
      expectedSessionId: 'session-a',
      sessionId: 'fork-a',
      title: 'Fork A',
      explicitSelection: true,
      activate: true,
      seq: 1,
    }

    expect(origin.params.sessionId).toBe('fork-a')
    expect(origin.title).toBe('Fork A')
    expect(other.params.sessionId).toBe('session-b')
    expect(dock.activePanel?.id).toBe(other.id)
  })

  it('keeps a pinned Fork source and opens the Fork in the same group', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    store.openSessionChatPinned({ sessionId: 'session-a', title: 'A' })
    const origin = dock.activePanel!

    chatStoreMock.forkedSessionRequested.value = {
      botId: 'bot-1',
      viewId: origin.id,
      expectedSessionId: 'session-a',
      sessionId: 'fork-a',
      title: 'Fork A',
      explicitSelection: true,
      activate: true,
      seq: 1,
    }

    const fork = dock.panels.find(panel => panel.params.sessionId === 'fork-a')!
    expect(origin.params.sessionId).toBe('session-a')
    expect(fork.group?.id).toBe(origin.group?.id)
    expect(store.ephemeralPanels[fork.id]).toBe(true)
    expect(dock.activePanel?.id).toBe(fork.id)
  })

  it('ignores a Fork result after its origin has been rebound', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    store.openSessionChat({ sessionId: 'session-a', title: 'A' })
    const origin = dock.activePanel!
    store.openSessionChat({ sessionId: 'session-c', title: 'C', groupId: origin.group!.id })

    chatStoreMock.forkedSessionRequested.value = {
      botId: 'bot-1',
      viewId: origin.id,
      expectedSessionId: 'session-a',
      sessionId: 'fork-a',
      title: 'Fork A',
      explicitSelection: true,
      activate: true,
      seq: 1,
    }

    expect(origin.params.sessionId).toBe('session-c')
    expect(dock.panels.some(panel => panel.params.sessionId === 'fork-a')).toBe(false)
  })

  it('routes a Fork only to the same-Session split where it was requested', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    store.openSessionChat({ sessionId: 'session-a', title: 'A' })
    const left = dock.activePanel!
    store.splitGroup(left.group!.id, 'right')
    const right = dock.activePanel!

    chatStoreMock.forkedSessionRequested.value = {
      botId: 'bot-1',
      viewId: right.id,
      expectedSessionId: 'session-a',
      sessionId: 'fork-right',
      title: 'Fork right',
      explicitSelection: true,
      activate: true,
      seq: 1,
    }

    expect(left.params.sessionId).toBe('session-a')
    expect(right.params.sessionId).toBe('fork-right')
    expect(dock.activePanel?.id).toBe(right.id)
  })

  it('opens a related Session in the originating split instead of the focused split', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)
    store.openSessionChat({ sessionId: 'session-a', title: 'A' })
    const origin = dock.activePanel!
    store.splitGroup(origin.group!.id, 'right')
    const other = dock.activePanel!
    store.openSessionChat({ sessionId: 'session-b', title: 'B', groupId: other.group!.id })

    store.openSessionChatFromView({
      viewId: origin.id,
      sessionId: 'child-a',
      title: 'Child A',
      expectedSessionId: 'session-a',
    })

    expect(dock.getPanel(origin.id)?.params.sessionId).toBe('child-a')
    expect(dock.getPanel(origin.id)?.title).toBe('Child A')
    expect(dock.getPanel(other.id)?.params.sessionId).toBe('session-b')
    expect(dock.activePanel?.id).toBe(origin.id)
  })

  it('restores two Chat panels without crossing their Session params', async () => {
    localStorage.setItem('workspace-layout', JSON.stringify({
      'bot-1': {
        layout: {
          grid: {
            root: {
              type: 'branch',
              data: [
                {
                  type: 'leaf',
                  size: 600,
                  data: { id: 'left', views: ['chat:1'], activeView: 'chat:1' },
                },
                {
                  type: 'leaf',
                  size: 600,
                  data: { id: 'right', views: ['chat:2'], activeView: 'chat:2' },
                },
              ],
            },
            width: 1200,
            height: 800,
            orientation: 'HORIZONTAL',
          },
          panels: {
            'chat:1': {
              id: 'chat:1',
              contentComponent: 'chat',
              title: 'A',
              params: { sessionId: 'session-a', explicitSelection: true },
            },
            'chat:2': {
              id: 'chat:2',
              contentComponent: 'chat',
              title: 'B',
              params: { sessionId: 'session-b', explicitSelection: true },
            },
          },
          activeGroup: 'left',
        },
        chatCounter: 2,
        ephemeralIds: [],
      },
    }))
    chatStoreMock.sessionId = 'session-a'
    chatStoreMock.hasExplicitSessionSelection = true
    useChatSelectionStore().setSession('session-a')
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()

    store.registerApi(dock as never)
    await nextTick()

    expect(dock.getPanel('chat:1')?.params.sessionId).toBe('session-a')
    expect(dock.getPanel('chat:2')?.params.sessionId).toBe('session-b')
    expect(dock.getPanel('chat:1')?.group?.id).not.toBe(dock.getPanel('chat:2')?.group?.id)
    expect(chatStoreMock.focusChatView).toHaveBeenLastCalledWith('chat:1')
  })

  it('opens multiple schedule panels and focuses an existing schedule', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openSchedule('schedule-a', 'Morning')
    store.openSchedule('schedule-b', 'Evening')
    store.openSchedule('schedule-a', 'Morning renamed')

    expect(dock.panels.filter((p) => p.component === 'schedule')).toHaveLength(2)
    expect(dock.activePanel?.id).toBe('schedule:schedule-a')
    expect(dock.getPanel('schedule:schedule-a')?.title).toBe('Morning renamed')
  })

  it('tracks file dirty state without mangling the tab title', () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openFile('/data/notes/todo.md')
    const panel = dock.getPanel('file:/data/notes/todo.md')
    expect(panel?.title).toBe('todo.md')

    store.setFileDirty('file:/data/notes/todo.md', true)
    expect(store.fileDirty['file:/data/notes/todo.md']).toBe(true)
    expect(store.dirtyFileCount).toBe(1)
    // Title stays the clean base name — the dot is now a tab-rendered affordance.
    expect(panel?.title).toBe('todo.md')

    store.setFileDirty('file:/data/notes/todo.md', false)
    expect(store.fileDirty['file:/data/notes/todo.md']).toBeUndefined()
    expect(store.dirtyFileCount).toBe(0)
  })

  it('queues a dirty tab for confirmation instead of closing it', async () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openFile('/data/a.md')
    store.setFileDirty('file:/data/a.md', true)

    // A dirty close is blocked; the tab is queued for the confirm dialog.
    store.requestCloseTab('file:/data/a.md')
    expect(dock.getPanel('file:/data/a.md')).toBeTruthy()
    expect(store.pendingClose?.panelId).toBe('file:/data/a.md')
    expect(store.pendingClose?.title).toBe('a.md')

    // Discard closes it and clears the queue.
    await store.resolvePendingClose('discard')
    expect(dock.getPanel('file:/data/a.md')).toBeUndefined()
    expect(store.pendingClose).toBeNull()
  })

  it('saves a dirty tab via its handler before closing', async () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openFile('/data/b.md')
    store.setFileDirty('file:/data/b.md', true)
    const save = vi.fn(async () => true)
    store.registerFileSaveHandler('file:/data/b.md', save)

    store.requestCloseTab('file:/data/b.md')
    await store.resolvePendingClose('save')

    expect(save).toHaveBeenCalledOnce()
    expect(dock.getPanel('file:/data/b.md')).toBeUndefined()
  })

  it('keeps a dirty tab open when its save fails', async () => {
    const store = useWorkspaceTabsStore()
    const dock = createFakeDock()
    store.registerApi(dock as never)

    store.openFile('/data/c.md')
    store.setFileDirty('file:/data/c.md', true)
    store.registerFileSaveHandler('file:/data/c.md', async () => false)

    store.requestCloseTab('file:/data/c.md')
    await store.resolvePendingClose('save')

    // Save failed → tab stays, but it leaves the queue so the dialog dismisses.
    expect(dock.getPanel('file:/data/c.md')).toBeTruthy()
    expect(store.pendingClose).toBeNull()
  })

  it('switches the active view and keeps the sidebar open', () => {
    const store = useWorkspaceTabsStore()

    store.sidebarView = 'sessions'
    store.sidebarOpen = true

    store.selectSidebarView('files')
    expect(store.sidebarView).toBe('files')
    expect(store.sidebarOpen).toBe(true)

    // Re-selecting the active view keeps the sidebar open instead of toggling it closed.
    store.selectSidebarView('files')
    expect(store.sidebarView).toBe('files')
    expect(store.sidebarOpen).toBe(true)
  })

  it('keeps activity panel collapse separate from whole workbench collapse', () => {
    const store = useWorkspaceTabsStore()

    store.sidebarView = 'sessions'
    store.sidebarOpen = true
    store.workbenchOpen = true

    store.toggleWorkbench()
    expect(store.workbenchOpen).toBe(false)
    expect(store.sidebarOpen).toBe(true)

    store.selectSidebarView('files')
    expect(store.workbenchOpen).toBe(true)
    expect(store.sidebarOpen).toBe(true)
    expect(store.sidebarView).toBe('files')

    store.hideWorkbench()
    expect(store.workbenchOpen).toBe(false)

    store.showWorkbench()
    expect(store.workbenchOpen).toBe(true)
  })

  it('drops legacy persisted tab models', () => {
    localStorage.setItem('workspace-tabs', '{"bot-1":{"tabs":[]}}')
    localStorage.setItem('workspace-panes', '{"bot-1":{"panes":[]}}')
    useWorkspaceTabsStore()
    expect(localStorage.getItem('workspace-tabs')).toBeNull()
    expect(localStorage.getItem('workspace-panes')).toBeNull()
  })

  // The mobile single-stack constraint (useIsMobile): below 768px the dock is
  // one group with a hidden tab strip, splits are vetoed or degraded to tabs,
  // and the persisted desktop layout is neither restored nor overwritten.
  // mobileBreakpoint simulates the media query; reset between tests so a
  // mobile state can never leak into the desktop suite.
  describe('mobile single-stack constraint', () => {
    afterEach(() => {
      mobileBreakpoint.reset()
    })

    it('ignores the persisted desktop layout and cold-starts a single hidden-header group', async () => {
      mobileBreakpoint.setMobile(true)
      const storedLayout = persistedLayoutWithPanel(
        'file:1',
        'file',
        { filePath: '/data/a.ts' },
        'a.ts',
      )
      const storedJson = JSON.stringify({ 'bot-1': { layout: storedLayout, ephemeralIds: [] } })
      localStorage.setItem('workspace-layout', storedJson)

      const store = useWorkspaceTabsStore()
      const dock = createFakeDock()
      store.registerApi(dock as never)
      await flushDraftChatFallback()

      // The desktop tree is NOT deserialized: no file panel, one group, and
      // the draft fallback owns the stack.
      expect(dock.getPanel('file:1')).toBeUndefined()
      expect(dock.groups).toHaveLength(1)
      expect(dock.panels).toHaveLength(1)
      expect(dock.panels[0]!.component).toBe('chat')
      expect(dock.groups[0]!.model.header.hidden).toBe(true)
      // The stored LAYOUT tree is untouched: mobile never restores it and
      // never serializes over it. Per-bot counters living in the same record
      // (chatCounter for the draft spawn) may still tick — they number new
      // panels, they are not the desktop arrangement.
      const written = JSON.parse(localStorage.getItem('workspace-layout')!)
      expect(written['bot-1'].layout).toEqual(storedLayout)
    })

    it('keeps programmatic opens inside the single group instead of splitting', async () => {
      mobileBreakpoint.setMobile(true)
      const store = useWorkspaceTabsStore()
      const dock = createFakeDock()
      store.registerApi(dock as never)
      await flushDraftChatFallback()

      store.openTerminal()
      expect(dock.getPanel('terminal:1')).toBeTruthy()
      expect(dock.groups).toHaveLength(1)

      store.openFile('/data/notes/todo.md')
      store.splitGroup('group-1', 'right')
      expect(dock.getPanel('file:/data/notes/todo.md~2')).toBeTruthy()
      expect(dock.groups).toHaveLength(1)
      expect(dock.groups[0]!.panels.map(panel => panel.id).sort()).toEqual([
        'file:/data/notes/todo.md',
        'file:/data/notes/todo.md~2',
        'terminal:1',
        ...dock.panels.filter(panel => panel.component === 'chat').map(panel => panel.id),
      ].sort())
    })

    it('merges groups and hides the tab strip on entry, restores the strip on exit', async () => {
      const store = useWorkspaceTabsStore()
      const dock = createFakeDock()
      store.registerApi(dock as never)
      store.openFile('/data/notes/todo.md')
      store.splitGroup('group-1', 'right')
      expect(dock.groups).toHaveLength(2)

      mobileBreakpoint.setMobile(true)
      await nextTick()
      expect(dock.groups).toHaveLength(1)
      expect(dock.groups[0]!.model.header.hidden).toBe(true)

      mobileBreakpoint.setMobile(false)
      await nextTick()
      expect(dock.groups[0]!.model.header.hidden).toBe(false)
    })

    it('vetoes drop overlays and drops that would split off a new group', () => {
      mobileBreakpoint.setMobile(true)
      const store = useWorkspaceTabsStore()
      const dock = createFakeDock()
      store.registerApi(dock as never)

      const overlay = {
        position: 'right',
        group: dock.groups[0]!,
        getData: () => undefined,
        preventDefault: vi.fn(),
      }
      dock.emitWillShowOverlay(overlay)
      expect(overlay.preventDefault).toHaveBeenCalled()

      const drop = {
        position: 'below',
        group: dock.groups[0]!,
        getData: () => undefined,
        preventDefault: vi.fn(),
      }
      dock.emitWillDrop(drop)
      expect(drop.preventDefault).toHaveBeenCalled()

      // A center drop merges as a tab — the only arrangement mobile allows.
      const center = {
        position: 'center',
        group: dock.groups[0]!,
        getData: () => undefined,
        preventDefault: vi.fn(),
      }
      dock.emitWillShowOverlay(center)
      expect(center.preventDefault).not.toHaveBeenCalled()
    })

    it('never serializes the mobile stack over the desktop layout snapshot', async () => {
      mobileBreakpoint.setMobile(true)
      const storedLayout = persistedLayoutWithPanel(
        'chat:bot-1:7',
        'chat',
        { sessionId: null, explicitSelection: true },
        'Old Session',
      )
      const storedJson = JSON.stringify({ 'bot-1': { layout: storedLayout, ephemeralIds: [] } })
      localStorage.setItem('workspace-layout', storedJson)

      const store = useWorkspaceTabsStore()
      const dock = createFakeDock()
      store.registerApi(dock as never)
      await flushDraftChatFallback()
      store.openTerminal()
      await nextTick()

      // The mobile stack (draft chat + terminal) must NOT be serialized over
      // the desktop snapshot: the layout tree round-trips untouched. The
      // terminalCounter bump sharing the record is expected — it numbers the
      // new panel, it does not reshape the desktop arrangement.
      const written = JSON.parse(localStorage.getItem('workspace-layout')!)
      expect(written['bot-1'].layout).toEqual(storedLayout)
    })

    it('keeps the desktop snapshot sealed after leaving mobile until the next desktop restore', async () => {
      const storedLayout = persistedLayoutWithPanel(
        'chat:bot-1:7',
        'chat',
        { sessionId: null, explicitSelection: true },
        'Old Session',
      )
      localStorage.setItem('workspace-layout', JSON.stringify({
        'bot-1': { layout: storedLayout, ephemeralIds: [] },
      }))

      // Start on DESKTOP so the snapshot is deserialized with persistence
      // live; the restore round-trips it back into storage. That
      // round-tripped form (not the seed above) is the baseline — the exit
      // path must leave exactly it untouched.
      const store = useWorkspaceTabsStore()
      const dock = createFakeDock()
      store.registerApi(dock as never)
      await flushDraftChatFallback()
      const desktopPersistedLayout = JSON.parse(localStorage.getItem('workspace-layout')!)['bot-1'].layout

      // Enter mobile and open a terminal: the stack diverges from the
      // snapshot (persist blocked by isMobile).
      mobileBreakpoint.setMobile(true)
      await nextTick()
      store.openTerminal()
      await nextTick()

      // Exit mobile: only the tab strip comes back — the dock is STILL the
      // merged stack, so the isMobile guard no longer covers it. Any layout
      // change in this window (here a file open; in the wild a tab
      // activation or an async panel title) must not serialize the merged
      // stack over the desktop snapshot.
      mobileBreakpoint.setMobile(false)
      await nextTick()
      store.openFile('/data/notes/todo.md')
      await flushDraftChatFallback()

      const written = JSON.parse(localStorage.getItem('workspace-layout')!)
      expect(written['bot-1'].layout).toEqual(desktopPersistedLayout)
    })

    it('focuses the existing terminal instead of stacking unreachable new ones', async () => {
      mobileBreakpoint.setMobile(true)
      const store = useWorkspaceTabsStore()
      const dock = createFakeDock()
      store.registerApi(dock as never)
      await flushDraftChatFallback()

      store.openTerminal()
      store.openTerminal()
      store.openTerminal()

      // The mobile shell has no tab strip to enumerate open panels and ←
      // only re-activates chat, so every "New Terminal" after the first must
      // focus the existing one — a second terminal would stay alive but
      // unreachable and unclosable, leaking its shell.
      expect(dock.panels.filter(panel => panel.id.startsWith('terminal:'))).toHaveLength(1)
      expect(dock.activePanel?.id).toBe('terminal:1')
    })

    it('focuses the existing browser instead of stacking unreachable new ones', async () => {
      mobileBreakpoint.setMobile(true)
      const store = useWorkspaceTabsStore()
      const dock = createFakeDock()
      store.registerApi(dock as never)
      await flushDraftChatFallback()

      store.openBrowser()
      store.activateChatPanel()
      store.openBrowser()
      store.activateChatPanel()
      store.openBrowser()

      expect(dock.panels.filter(panel => panel.id.startsWith('browser:'))).toHaveLength(1)
      expect(dock.activePanel?.id).toBe('browser:1')
    })

    it('never opens or focuses Desktop when a GUI tool starts on mobile', async () => {
      mobileBreakpoint.setMobile(true)
      const store = useWorkspaceTabsStore()
      const dock = createFakeDock()
      store.registerApi(dock as never)
      await flushDraftChatFallback()

      // The runtime fires one guiToolUseRequested per GUI tool CALL, so a turn
      // with several calls used to re-open/re-focus the viewer every time —
      // on the single stack each focus steals the whole screen from chat.
      const chatPanelId = dock.activePanel?.id
      emitGuiToolUse()
      emitGuiToolUse('computer_observe')

      expect(dock.getPanel('display:1')).toBeUndefined()
      expect(dock.activePanel?.id).toBe(chatPanelId)

      // Even a Desktop the user opened MANUALLY must not be re-focused by
      // later tool calls: after going back to chat, chat stays on top.
      store.openDisplay()
      expect(dock.activePanel?.id).toBe('display:1')
      store.activateChatPanel()
      emitGuiToolUse()
      expect(dock.activePanel?.id).not.toBe('display:1')
      expect(dock.activePanel?.id).toBe(chatPanelId)
    })
  })
})
