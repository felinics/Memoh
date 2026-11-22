import { defineStore, storeToRefs } from 'pinia'
import { computed, nextTick, ref, shallowRef, watch } from 'vue'
import { useLocalStorage } from '@vueuse/core'
import { useTabScopedStorage } from '@/utils/tab-scoped-storage'
import { useCoarsePointer, useCompactShell } from '@/composables/useEnvironment'
import { focusIsInTextInput } from '@/lib/keyboard-context'
import { useChatStore } from '@/store/chat-list'
import { useChatSelectionStore } from '@/store/chat-selection'
import { onAuthSessionCleared } from '@/lib/auth-session'
import { hasBotPermission, type BotPermission } from '@/utils/bot-permissions'
import { parseBrowserAddress } from '@/utils/browser-address'
import { activeWorkbenchDrag, cancelActiveWorkbenchDrag, startWorkbenchExternalPointerDrag } from '@/workbench/drag'
import {
  clearTerminalSnapshots,
  clearTerminalSnapshotsForBot,
  deleteTerminalSnapshot,
  terminalCacheKey,
} from '@/composables/useTerminalCache'
import type { OpenAssetPreviewArgs } from '@/pages/home/composables/useFileManagerProvider'
import {
  activateGroup as treeActivateGroup,
  activateTab as treeActivateTab,
  createEmptyWorkbenchState,
  findGroup,
  findGroupOfTab,
  findRightNeighborOfSpatialFirst,
  findSpatialFirstGroupId,
  insertTab,
  keepOpenTab as treeKeepOpenTab,
  listGroups,
  markPreviewTab,
  moveTabToGroup,
  parseWorkbenchState,
  removeTab,
  reorderTabInGroup,
  setSplitRatio as treeSetSplitRatio,
  setTabPinned,
  splitGroupWithTab,
  updateTabTarget,
} from '@/workbench/tree'
import { resolveWorkbenchTabTitle } from '@/workbench/titles'
import type {
  WorkbenchDropZone,
  WorkbenchSplitDirection,
  WorkbenchState,
  WorkbenchTab,
} from '@/workbench/types'

export type SidebarView = 'sessions' | 'files' | 'schedule' | 'supermarket'

export const DISPLAY_PANEL_ID = 'display:1'

const DEFAULT_BROWSER_ADDRESS = 'localhost:5173/'
const WORKBENCH_STORAGE_KEY = 'memoh:workbench:v1'
const LEGACY_STORAGE_KEYS = ['workspace-tabs', 'workspace-panes', 'workspace-layout'] as const
const CHAT_DRAFTS_STORAGE_KEY = 'chat-input-drafts'

const ENTRY_OVERLAY = '[role="menu"], [role="menubar"], [role="dialog"], [role="alertdialog"], [role="listbox"]'
const ENTRY_OVERLAY_MAX_FRAMES = 120
const ENTRY_FOCUS_RETURN_MAX_FRAMES = 6

interface BotWorkbenchRecord {
  workbench: WorkbenchState | null
  terminalCounter: number
  browserCounter: number
  scheduleCounter: number
  chatCounter: number
}

type WorkbenchStorageMap = Record<string, BotWorkbenchRecord>
type NumberedTabKind = 'chat' | 'terminal' | 'browser' | 'schedule'

const COUNTER_KEY: Record<NumberedTabKind, keyof Omit<BotWorkbenchRecord, 'workbench'>> = {
  chat: 'chatCounter',
  terminal: 'terminalCounter',
  browser: 'browserCounter',
  schedule: 'scheduleCounter',
}

function emptyBotRecord(): BotWorkbenchRecord {
  return { workbench: null, terminalCounter: 0, browserCounter: 0, scheduleCounter: 0, chatCounter: 0 }
}

function safeCounter(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? Math.floor(value) : 0
}

function numberedTabId(kind: NumberedTabKind, n: number): string {
  return kind === 'schedule' ? `schedule:new:${n}` : `${kind}:${n}`
}

function maxCountersInState(state: WorkbenchState | null | undefined) {
  const max = { chat: 0, terminal: 0, browser: 0, schedule: 0 }
  const has = { terminal: false, browser: false }
  if (!state || typeof state !== 'object' || !state.tabs || typeof state.tabs !== 'object') {
    return { max, has }
  }
  for (const tab of Object.values(state.tabs)) {
    if (!tab || typeof tab !== 'object' || !tab.target || typeof tab.target !== 'object') continue
    if (tab.target.kind === 'terminal') {
      has.terminal = true
      max.terminal = Math.max(max.terminal, safeCounter(tab.target.instance))
    } else if (tab.target.kind === 'browser') {
      has.browser = true
      max.browser = Math.max(max.browser, safeCounter(tab.target.instance))
    } else if (tab.target.kind === 'chat') {
      const m = /^chat:(\d+)$/.exec(tab.id)
      if (m) max.chat = Math.max(max.chat, safeCounter(Number(m[1])))
    } else if (tab.target.kind === 'schedule' && !tab.target.scheduleId) {
      const m = /^schedule:new:(\d+)$/.exec(tab.id)
      if (m) max.schedule = Math.max(max.schedule, safeCounter(Number(m[1])))
    }
  }
  return { max, has }
}

export const useWorkspaceTabsStore = defineStore('workspace-tabs', () => {
  const selection = useChatSelectionStore()
  const { currentBotId } = storeToRefs(selection)
  const chatStore = useChatStore()
  const currentBot = computed(() => chatStore.bots.find(bot => bot.id === currentBotId.value) ?? null)

  // Compact shell (see useCompactShell): below 768px the workbench runs as a
  // single stack — one group, no tab strip, no splits. Layout constraints in
  // this store key off `isMobile` (retained store property name for callers);
  // input focus keys off `coarsePointer` so wide tablets/landscape phones do
  // not pop up a software keyboard while keeping the multi-panel layout.
  const isMobile = useCompactShell()
  const coarsePointer = useCoarsePointer()

  if (typeof localStorage !== 'undefined') {
    const hasSessionWorkbench = typeof sessionStorage !== 'undefined' && sessionStorage.getItem(WORKBENCH_STORAGE_KEY) !== null
    const upgradingFromLegacyLayout = localStorage.getItem('workspace-layout') !== null
      && localStorage.getItem(WORKBENCH_STORAGE_KEY) === null
      && !hasSessionWorkbench
    for (const key of LEGACY_STORAGE_KEYS) localStorage.removeItem(key)
    if (upgradingFromLegacyLayout) localStorage.removeItem(CHAT_DRAFTS_STORAGE_KEY)
  }
  if (typeof sessionStorage !== 'undefined') {
    for (const key of LEGACY_STORAGE_KEYS) sessionStorage.removeItem(key)
  }

  const storage = useTabScopedStorage<WorkbenchStorageMap>(WORKBENCH_STORAGE_KEY, {}, { seed: true })

  const ready = ref(false)
  const workbench = shallowRef<WorkbenchState>(createEmptyWorkbenchState())
  const loadedBotId = ref<string | null>(null)
  const panelDragging = ref(false)
  const fileDirty = ref<Record<string, boolean>>({})
  const saveHandlers = new Map<string, { botId: string, handler: () => Promise<boolean> }>()
  const closeQueue = ref<string[]>([])

  let suppressPersist = false
  // Set when restore ran while mobile so phone cold-starts never overwrite a desktop snapshot.
  let mobileColdStart = false
  let pendingDragPersist = false
  const deletedSessionIdsByBot = new Map<string, Set<string>>()

  /** Mounted + bot-aligned guard: prevents pre-flush cross-bot writes while watch(currentBotId) is pending. */
  function activeBotId(): string | null {
    if (!ready.value) return null
    const bid = (currentBotId.value ?? '').trim()
    return bid && loadedBotId.value === bid ? bid : null
  }

  function ensureBotRecord(botId: string | null | undefined): BotWorkbenchRecord | null {
    const bid = (botId ?? '').trim()
    if (!bid) return null
    const existing = storage.value[bid]
    if (!existing) {
      const fresh = emptyBotRecord()
      storage.value = { ...storage.value, [bid]: fresh }
      return fresh
    }
    const { max } = maxCountersInState(existing.workbench)
    const terminalCounter = Math.max(safeCounter(existing.terminalCounter), max.terminal)
    const browserCounter = Math.max(safeCounter(existing.browserCounter), max.browser)
    const scheduleCounter = Math.max(safeCounter(existing.scheduleCounter), max.schedule)
    const chatCounter = Math.max(safeCounter(existing.chatCounter), max.chat)
    if (
      terminalCounter !== existing.terminalCounter
      || browserCounter !== existing.browserCounter
      || scheduleCounter !== existing.scheduleCounter
      || chatCounter !== existing.chatCounter
    ) {
      const normalized = { ...emptyBotRecord(), ...existing, terminalCounter, browserCounter, scheduleCounter, chatCounter }
      storage.value = { ...storage.value, [bid]: normalized }
      return normalized
    }
    return existing
  }

  function patchBotRecord(botId: string, patch: Partial<BotWorkbenchRecord>) {
    const record = ensureBotRecord(botId)
    if (!record) return
    storage.value = { ...storage.value, [botId]: { ...record, ...patch } }
  }

  function allocateTabNumber(bid: string, kind: NumberedTabKind, activeState: WorkbenchState = workbench.value): number {
    const record = ensureBotRecord(bid) ?? emptyBotRecord()
    const key = COUNTER_KEY[kind]
    const active = maxCountersInState(activeState)
    const stored = maxCountersInState(record.workbench)
    const preserveMobileCounters = isMobile.value || mobileColdStart

    let next: number
    if ((kind === 'terminal' || kind === 'browser') && !active.has[kind] && !(preserveMobileCounters && stored.has[kind])) {
      next = 1
    } else {
      next = Math.max(safeCounter(record[key]), active.max[kind], stored.max[kind]) + 1
      while (activeState.tabs[numberedTabId(kind, next)] || record.workbench?.tabs?.[numberedTabId(kind, next)]) {
        next += 1
      }
    }
    const recorded = preserveMobileCounters ? Math.max(safeCounter(record[key]), next) : next
    patchBotRecord(bid, { [key]: recorded })
    return next
  }

  const activeGroup = computed(() => {
    if (!ready.value) return null
    return findGroup(workbench.value.root, workbench.value.activeGroupId)
      ?? listGroups(workbench.value.root)[0]
      ?? null
  })
  const activeGroupId = computed<string | null>(() => activeGroup.value?.id ?? null)
  const activeId = computed<string | null>(() => (ready.value ? activeGroup.value?.activeTabId ?? null : null))
  const activeTab = computed<WorkbenchTab | null>(() => (activeId.value ? workbench.value.tabs[activeId.value] ?? null : null))
  const spatialFirstGroupId = computed<string | null>(() => (ready.value ? findSpatialFirstGroupId(workbench.value.root) : null))
  const activePanelIsChat = computed(() => !activeTab.value || activeTab.value.target.kind === 'chat')

  function persistWorkbench() {
    if (!ready.value || suppressPersist || !loadedBotId.value || isMobile.value || mobileColdStart) return
    if (panelDragging.value) {
      pendingDragPersist = true
      return
    }
    pendingDragPersist = false
    patchBotRecord(loadedBotId.value, { workbench: workbench.value })
  }

  watch(panelDragging, (dragging) => {
    if (!dragging && pendingDragPersist) {
      pendingDragPersist = false
      persistWorkbench()
    }
  })

  function selectChatSession(sid: string, explicitSelection: boolean) {
    void (explicitSelection
      ? chatStore.selectSession(sid)
      : chatStore.selectSession(sid, { explicitSelection: false }))
  }

  function tabSessionId(tab: WorkbenchTab | undefined | null): string | null {
    if (!tab || tab.target.kind !== 'chat') return null
    return tab.target.sessionId?.trim() || null
  }

  function chatTabForSession(sid: string, state: WorkbenchState = workbench.value): WorkbenchTab | undefined {
    return Object.values(state.tabs).find(tab => tab.target.kind === 'chat' && tabSessionId(tab) === sid)
  }

  function isDeletedSessionForCurrentBot(sid: string | null): boolean {
    const bid = (currentBotId.value ?? '').trim()
    if (!bid || !sid) return false
    const latestDeleted = chatStore.deletedSession
    if (latestDeleted?.botId === bid && latestDeleted.id === sid) return true
    return Boolean(deletedSessionIdsByBot.get(bid)?.has(sid))
  }

  function activateChatSession(tab: WorkbenchTab, explicitOverride?: boolean) {
    if (suppressPersist || tab.target.kind !== 'chat') return
    const sid = tabSessionId(tab)
    if (sid && isDeletedSessionForCurrentBot(sid)) return
    chatStore.focusChatView(tab.id)
    if (sid) {
      selectChatSession(sid, explicitOverride ?? tab.target.explicitSelection)
    } else {
      const explicitSelection = explicitOverride
        ?? (chatStore.hasExplicitSessionSelection === true || tab.target.explicitSelection === true)
      chatStore.selectDraft({ explicitSelection })
    }
  }

  function cleanupPanelState(id: string) {
    if (fileDirty.value[id]) {
      const next = { ...fileDirty.value }
      delete next[id]
      fileDirty.value = next
    }
    saveHandlers.delete(id)
    if (closeQueue.value.includes(id)) {
      closeQueue.value = closeQueue.value.filter(q => q !== id)
    }
  }

  function activeTabOfState(state: WorkbenchState): WorkbenchTab | null {
    const group = findGroup(state.root, state.activeGroupId) ?? listGroups(state.root)[0]
    return group?.activeTabId ? state.tabs[group.activeTabId] ?? null : null
  }

  function buildEmptyWorkspaceFallbackState(
    baseState: WorkbenchState,
    bid: string,
    opts?: { forceDraft?: boolean },
  ): { state: WorkbenchState, explicitSelection: boolean } {
    const explicitSelection = opts?.forceDraft ? false : chatStore.hasExplicitSessionSelection === true
    const sid = opts?.forceDraft ? '' : (chatStore.sessionId ?? '').trim()
    const id = `chat:${allocateTabNumber(bid, 'chat', baseState)}`
    const tab: WorkbenchTab = {
      id,
      pinned: false,
      target: {
        kind: 'chat',
        sessionId: sid && explicitSelection && !isDeletedSessionForCurrentBot(sid) ? sid : null,
        explicitSelection,
      },
    }
    return {
      explicitSelection,
      state: insertTab(baseState, tab, {
        preview: true,
        activate: true,
        canReplacePreview: tabId => !fileDirty.value[tabId],
      }),
    }
  }

  /** Single state-commit funnel: cleans removed tabs, atomically refills empty workspaces, syncs chat, and persists. */
  function commitWorkbench(nextState: WorkbenchState, explicitOverride?: boolean) {
    const prevState = workbench.value
    if (nextState === prevState) return

    const prevActive = activeTabOfState(prevState)
    const removedTabs = Object.values(prevState.tabs).filter(tab => !nextState.tabs[tab.id])

    const bid = (loadedBotId.value ?? '').trim()
    const shouldRefillEmpty = ready.value && !suppressPersist && Boolean(bid) && Object.keys(nextState.tabs).length === 0
    const hadRemovedChat = removedTabs.some(t => t.target.kind === 'chat')
    const refill = shouldRefillEmpty
      ? buildEmptyWorkspaceFallbackState(nextState, bid, { forceDraft: hadRemovedChat })
      : null
    const committedState = refill ? refill.state : nextState

    workbench.value = committedState
    const nextActive = activeTabOfState(committedState)

    const drag = activeWorkbenchDrag.value
    const draggedTabId = drag?.railTab?.tabId ?? drag?.detachedTabId ?? null
    if (draggedTabId && removedTabs.some(t => t.id === draggedTabId)) {
      cancelActiveWorkbenchDrag()
    }

    for (const removed of removedTabs) {
      cleanupPanelState(removed.id)
      if (removed.target.kind === 'terminal' && loadedBotId.value) {
        const removedBid = loadedBotId.value
        void nextTick(() => deleteTerminalSnapshot(terminalCacheKey(removedBid, removed.id)))
      }
    }

    if (hadRemovedChat && !suppressPersist) {
      const anyChatRemaining = Object.values(committedState.tabs).some(t => t.target.kind === 'chat')
      if (!anyChatRemaining) {
        chatStore.selectDraft({ explicitSelection: false })
      } else if (!shouldRefillEmpty) {
        const sid = (chatStore.sessionId ?? '').trim()
        const closedSelectedSid = removedTabs.some(t => tabSessionId(t) === sid)
        if (sid && closedSelectedSid && !chatTabForSession(sid, committedState) && (!nextActive || nextActive.target.kind !== 'chat')) {
          chatStore.resetToEmptyComposer({ clearPendingExternalAgent: false, explicitSelection: false, draftIntent: false })
        }
      }
    }

    if (nextActive && nextActive.id !== prevActive?.id && nextActive.target.kind === 'chat') {
      activateChatSession(nextActive, refill ? refill.explicitSelection : explicitOverride)
    }
    if (shouldRefillEmpty) focusChatInputAfterEntry()
    persistWorkbench()
  }

  function hasCurrentPermission(permission: BotPermission): boolean {
    return hasBotPermission(currentBot.value?.current_user_permissions, permission)
  }

  function isTabAllowed(tab: WorkbenchTab): boolean {
    switch (tab.target.kind) {
      case 'chat':
      case 'asset':
        return true
      case 'file':
      case 'preview':
        return hasCurrentPermission('workspace_read')
      case 'terminal':
        return hasCurrentPermission('workspace_exec')
      case 'browser':
      case 'display':
      case 'schedule':
        return hasCurrentPermission('manage')
    }
  }

  function pruneDisallowedTabs() {
    const perms = currentBot.value?.current_user_permissions
    if (!activeBotId() || !perms || perms.length === 0) return
    let next = workbench.value
    for (const tab of Object.values(next.tabs)) {
      if (!isTabAllowed(tab)) next = removeTab(next, tab.id)
    }
    if (next !== workbench.value) commitWorkbench(next)
  }

  function clearRuntimeState(opts?: { clearLoadedBot?: boolean, clearMobileColdStart?: boolean, clearSaveHandlers?: boolean }) {
    cancelActiveWorkbenchDrag()
    panelDragging.value = false
    pendingDragPersist = false
    if (opts?.clearMobileColdStart) mobileColdStart = false
    if (opts?.clearLoadedBot) loadedBotId.value = null
    if (opts?.clearSaveHandlers) saveHandlers.clear()
    if (!ready.value && !opts?.clearSaveHandlers) return
    suppressPersist = true
    try {
      workbench.value = createEmptyWorkbenchState()
      fileDirty.value = {}
      closeQueue.value = []
    } finally {
      suppressPersist = false
    }
  }

  function finalizeRestoredWorkbench(bid: string) {
    if (Object.keys(workbench.value.tabs).length === 0) {
      const fallback = buildEmptyWorkspaceFallbackState(workbench.value, bid)
      commitWorkbench(fallback.state, fallback.explicitSelection)
    } else {
      persistWorkbench()
    }
    syncDraftChatExplicitSelection()
    alignSelectionWithDock()
    focusChatInputAfterEntry()
  }

  function restoreForBot(botId: string) {
    if (!ready.value) return
    cancelActiveWorkbenchDrag()
    panelDragging.value = false
    pendingDragPersist = false
    suppressPersist = true
    try {
      const stored = storage.value[botId]?.workbench ?? null
      if (stored && !isMobile.value) {
        const parsed = parseWorkbenchState(stored)
        if (parsed) {
          workbench.value = parsed
          ensureBotRecord(botId)
        } else {
          workbench.value = createEmptyWorkbenchState()
          patchBotRecord(botId, { workbench: null })
        }
      } else {
        workbench.value = createEmptyWorkbenchState()
      }
      loadedBotId.value = botId
      mobileColdStart = isMobile.value
      fileDirty.value = {}
      closeQueue.value = []
      pruneDisallowedTabs()
      reconcileDeletedChatPanels()
    } finally {
      suppressPersist = false
    }
    finalizeRestoredWorkbench(botId)
  }

  function mountWorkspace() {
    ready.value = true
    const bid = (currentBotId.value ?? '').trim()
    if (bid) {
      ensureBotRecord(bid)
      restoreForBot(bid)
    }
  }

  function unmountWorkspace() {
    ready.value = false
    clearRuntimeState({ clearLoadedBot: true, clearMobileColdStart: true, clearSaveHandlers: true })
  }

  // ---- Focus management ----------------------------------------------------

  const pendingChatInputFocus = ref<{ panelId: string, botId: string, sessionId: string | null } | null>(null)
  watch([activeId, currentBotId, () => selection.sessionId], () => { pendingChatInputFocus.value = null }, { flush: 'sync' })

  function requestChatInputFocus() {
    if (!activeTab.value || activeTab.value.target.kind !== 'chat') activateChatPanel()
    requestActiveChatInputFocus()
  }

  function requestActiveChatInputFocus() {
    const tab = activeTab.value
    if (!tab || tab.target.kind !== 'chat' || !currentBotId.value) return
    pendingChatInputFocus.value = { panelId: tab.id, botId: currentBotId.value, sessionId: tab.target.sessionId }
  }

  // Fine-pointer environments: entering a conversation — cold start, a bot
  // switch, New Chat, a session opened from the sidebar, a menu or search —
  // leaves the caret in its composer. Requested a tick later, once the
  // activation and selection changes it rides on (which drop pending requests)
  // have settled. It never takes focus from another input, never switches away
  // from a non-chat tab, and never runs when the primary pointer is coarse
  // (phones, iPads, tablets, landscape phones), where programmatic focusing
  // inside a tap gesture raises a software keyboard the user did not ask for.
  let entryFocusRun = 0
  function focusChatInputAfterEntry() {
    if (coarsePointer.value) return
    const run = ++entryFocusRun
    void nextTick(() => {
      if (coarsePointer.value || run !== entryFocusRun) return
      const overlay = document.activeElement?.closest<HTMLElement>(ENTRY_OVERLAY)
      if (overlay) {
        afterOverlayReleasesFocus(overlay, run)
        return
      }
      if (focusIsInTextInput()) return
      requestActiveChatInputFocus()
    })
  }

  // ponytail: bounded RAF polling waits for Radix/Reka popovers to release focus (ceiling ~2s at 60Hz);
  // upgrade path: subscribe to a global overlay-closed bus if popovers ever need deterministic handoff.
  function afterOverlayReleasesFocus(
    overlay: HTMLElement,
    run: number,
    framesLeft = ENTRY_OVERLAY_MAX_FRAMES,
    returnFramesLeft = ENTRY_FOCUS_RETURN_MAX_FRAMES,
  ) {
    requestAnimationFrame(() => {
      if (run !== entryFocusRun || coarsePointer.value) return
      const active = document.activeElement
      if (overlay.isConnected && active && overlay.contains(active)) {
        if (framesLeft > 0) afterOverlayReleasesFocus(overlay, run, framesLeft - 1, returnFramesLeft)
        return
      }
      if (!overlay.isConnected && (!active || active === document.body) && returnFramesLeft > 0) {
        afterOverlayReleasesFocus(overlay, run, framesLeft, returnFramesLeft - 1)
        return
      }
      if (focusIsInTextInput()) return
      requestActiveChatInputFocus()
    })
  }

  // ---- Tab & split primitives ----------------------------------------------

  function activateTab(tabId: string) {
    if (!ready.value) return
    commitWorkbench(treeActivateTab(workbench.value, tabId))
  }

  function activateGroup(groupId: string) {
    if (!ready.value) return
    commitWorkbench(treeActivateGroup(workbench.value, groupId))
  }

  function focusAdjacentTab(direction: -1 | 1): boolean {
    if (!ready.value) return false
    const ids = isMobile.value
      ? listGroups(workbench.value.root).flatMap(g => g.tabIds)
      : (activeGroup.value?.tabIds ?? [])
    if (ids.length < 2) return false
    const idx = ids.indexOf((isMobile.value ? activeId.value : activeGroup.value?.activeTabId) ?? '')
    if (idx < 0) return false
    activateTab(ids[(idx + direction + ids.length) % ids.length]!)
    return true
  }

  function keepOpenTab(tabId: string) {
    if (!ready.value) return
    commitWorkbench(treeKeepOpenTab(workbench.value, tabId))
  }

  function pinTab(tabId: string) {
    if (!ready.value) return
    commitWorkbench(setTabPinned(workbench.value, tabId, true))
  }

  function unpinTab(tabId: string) {
    if (!ready.value) return
    commitWorkbench(setTabPinned(workbench.value, tabId, false))
  }

  function setSplitRatio(splitId: string, ratio: number) {
    if (!ready.value) return
    commitWorkbench(treeSetSplitRatio(workbench.value, splitId, ratio))
  }

  function reorderTab(groupId: string, tabId: string, targetIndex: number) {
    if (!ready.value) return
    commitWorkbench(reorderTabInGroup(workbench.value, groupId, tabId, targetIndex))
  }

  function moveTab(tabId: string, targetGroupId: string, zone: WorkbenchDropZone, insertIndex?: number) {
    if (!ready.value) return
    const effectiveZone: WorkbenchDropZone = isMobile.value && zone !== 'center' ? 'center' : zone
    commitWorkbench(moveTabToGroup(workbench.value, tabId, targetGroupId, effectiveZone, insertIndex))
  }

  function placeDroppedTab(tab: WorkbenchTab, targetGroupId: string, zone: WorkbenchDropZone, insertIndex?: number) {
    if (zone !== 'center') {
      commitWorkbench(splitGroupWithTab(workbench.value, targetGroupId, zone, tab, { preview: false, activate: true }))
    } else {
      let next = insertTab(workbench.value, tab, { groupId: targetGroupId, preview: false, activate: true })
      if (typeof insertIndex === 'number') next = reorderTabInGroup(next, targetGroupId, tab.id, insertIndex)
      commitWorkbench(next)
    }
  }

  function dropExternalItem(
    item: { kind: 'session', sessionId: string, title?: string } | { kind: 'file', filePath: string },
    targetGroupId: string,
    zone: WorkbenchDropZone,
    insertIndex?: number,
  ) {
    const bid = activeBotId()
    if (!bid) return
    const effectiveZone: WorkbenchDropZone = isMobile.value && zone !== 'center' ? 'center' : zone

    if (item.kind === 'session') {
      const sid = item.sessionId.trim()
      if (!sid || isDeletedSessionForCurrentBot(sid)) return
      focusChatInputAfterEntry()
      const existing = chatTabForSession(sid)
      if (existing && existing.target.kind === 'chat') {
        focusExistingSessionTab(existing, sid, {
          fallbackTitle: item.title?.trim() || undefined,
          explicitSelection: true,
          keepOpen: true,
          moveTo: { targetGroupId, zone: effectiveZone, insertIndex },
        })
        return
      }
      const id = `chat:${allocateTabNumber(bid, 'chat')}`
      placeDroppedTab(
        { id, pinned: false, target: { kind: 'chat', sessionId: sid, explicitSelection: true, fallbackTitle: item.title?.trim() || undefined } },
        targetGroupId,
        effectiveZone,
        insertIndex,
      )
      return
    }

    if (!hasCurrentPermission('workspace_read')) return
    const path = item.filePath.trim()
    if (!path) return
    const id = `file:${path}`
    if (workbench.value.tabs[id]) {
      let next = treeKeepOpenTab(workbench.value, id)
      next = moveTabToGroup(next, id, targetGroupId, effectiveZone, insertIndex)
      next = treeActivateTab(next, id)
      commitWorkbench(next)
      return
    }
    placeDroppedTab({ id, pinned: false, target: { kind: 'file', filePath: path } }, targetGroupId, effectiveZone, insertIndex)
  }

  function startExternalDrag(
    event: PointerEvent,
    item: { kind: 'session', sessionId: string, title?: string } | { kind: 'file', filePath: string },
    title: string,
  ) {
    if (isMobile.value || !ready.value) return
    startWorkbenchExternalPointerDrag({
      event,
      getTitle: () => title,
      isMobile: () => isMobile.value,
      getGroupTabIds: gid => findGroup(workbench.value.root, gid)?.tabIds ?? [],
      isTabPinned: tid => Boolean(workbench.value.tabs[tid]?.pinned),
      onDragStateChange: (dragging) => { panelDragging.value = dragging },
      onCommitDrop: (targetGid, zone, idx) => dropExternalItem(item, targetGid, zone, idx),
    })
  }

  function resolveTabTitle(tabId: string): string {
    const tab = workbench.value.tabs[tabId]
    return tab ? resolveWorkbenchTabTitle(tab.target, chatStore) : tabId
  }

  // ---- Chat & Session operations -------------------------------------------

  function openTab(
    tab: WorkbenchTab,
    opts?: { preview?: boolean, groupId?: string, activate?: boolean, explicitOverride?: boolean },
  ): boolean {
    if (!activeBotId()) return false
    const preview = opts?.preview ?? false
    const activate = opts?.activate !== false
    if (workbench.value.tabs[tab.id]) {
      let next = preview ? workbench.value : treeKeepOpenTab(workbench.value, tab.id)
      if (activate) next = treeActivateTab(next, tab.id)
      commitWorkbench(next, opts?.explicitOverride)
      return true
    }
    const next = insertTab(workbench.value, tab, {
      groupId: opts?.groupId,
      preview,
      activate: opts?.activate,
      canReplacePreview: preview ? id => !fileDirty.value[id] : undefined,
    })
    commitWorkbench(next, opts?.explicitOverride)
    return true
  }

  function focusExistingSessionTab(
    existing: WorkbenchTab,
    sid: string,
    opts: {
      fallbackTitle?: string
      explicitSelection: boolean
      keepOpen?: boolean
      activate?: boolean
      replaceTarget?: boolean
      moveTo?: { targetGroupId: string, zone: WorkbenchDropZone, insertIndex?: number }
    },
  ) {
    const activate = opts.activate !== false
    const wasAlreadyActive = activeId.value === existing.id
    let next = updateTabTarget(workbench.value, existing.id, t => t.kind === 'chat'
      ? {
          kind: 'chat',
          sessionId: sid,
          explicitSelection: opts.explicitSelection,
          fallbackTitle: opts.replaceTarget ? opts.fallbackTitle : (opts.fallbackTitle ?? t.fallbackTitle),
        }
      : t)
    if (opts.keepOpen) next = treeKeepOpenTab(next, existing.id)
    if (opts.moveTo) {
      next = moveTabToGroup(next, existing.id, opts.moveTo.targetGroupId, opts.moveTo.zone, opts.moveTo.insertIndex)
    }
    if (activate) {
      next = treeActivateTab(next, existing.id)
    }
    commitWorkbench(next, opts.explicitSelection)
    if (activate && wasAlreadyActive) {
      chatStore.focusChatView(existing.id)
      selectChatSession(sid, opts.explicitSelection)
    }
  }

  function openSessionChat(opts: {
    sessionId: string
    title?: string
    groupId?: string
    explicitSelection?: boolean
    groupLocal?: boolean
    activate?: boolean
  }) {
    const bid = activeBotId()
    const sid = opts.sessionId.trim()
    if (!bid || !sid || isDeletedSessionForCurrentBot(sid)) return

    const fallbackTitle = opts.title?.trim() || undefined
    const explicitSelection = opts.explicitSelection !== false
    const activate = opts.activate !== false
    if (activate) focusChatInputAfterEntry()

    const existing = opts.groupLocal && opts.groupId
      ? findGroup(workbench.value.root, opts.groupId)?.tabIds
          .map(id => workbench.value.tabs[id])
          .find((t): t is WorkbenchTab => Boolean(t && t.target.kind === 'chat' && tabSessionId(t) === sid))
      : chatTabForSession(sid)

    if (existing && existing.target.kind === 'chat') {
      focusExistingSessionTab(existing, sid, { fallbackTitle, explicitSelection, activate })
      return
    }

    const explicitGroup = opts.groupId ? findGroup(workbench.value.root, opts.groupId) : undefined
    const targetGroup = explicitGroup
      ?? (isMobile.value
        ? (activeGroup.value?.tabIds.some(id => workbench.value.tabs[id]?.target.kind === 'chat')
            ? activeGroup.value
            : listGroups(workbench.value.root).find(g => g.tabIds.some(id => workbench.value.tabs[id]?.target.kind === 'chat')) ?? activeGroup.value)
        : activeGroup.value)
    const reusableTab = targetGroup?.previewTabId ? workbench.value.tabs[targetGroup.previewTabId] : undefined
    if (reusableTab && reusableTab.target.kind === 'chat' && !isDeletedSessionForCurrentBot(tabSessionId(reusableTab))) {
      focusExistingSessionTab(reusableTab, sid, { fallbackTitle, explicitSelection, activate, replaceTarget: true })
      return
    }

    const id = `chat:${allocateTabNumber(bid, 'chat')}`
    openTab(
      { id, pinned: false, target: { kind: 'chat', sessionId: sid, explicitSelection, fallbackTitle } },
      { preview: true, groupId: opts.groupId ?? targetGroup?.id, activate, explicitOverride: explicitSelection },
    )
  }

  function openSessionChatFromView(opts: {
    viewId: string
    sessionId: string
    title?: string
    expectedSessionId?: string | null
    explicitSelection?: boolean
    activate?: boolean
  }) {
    if (!activeBotId()) return
    const origin = workbench.value.tabs[opts.viewId]
    const originGroup = findGroupOfTab(workbench.value.root, opts.viewId)
    if (!origin || !originGroup || origin.target.kind !== 'chat') return
    if ('expectedSessionId' in opts && tabSessionId(origin) !== opts.expectedSessionId) return
    openSessionChat({
      sessionId: opts.sessionId,
      title: opts.title,
      groupId: originGroup.id,
      explicitSelection: opts.explicitSelection,
      groupLocal: true,
      activate: opts.activate,
    })
  }

  function openSessionChatPinned(opts: { sessionId: string, title?: string, groupId?: string }) {
    const bid = activeBotId()
    const sid = opts.sessionId.trim()
    if (!bid || !sid || isDeletedSessionForCurrentBot(sid)) return
    const fallbackTitle = opts.title?.trim() || undefined
    focusChatInputAfterEntry()

    const existing = chatTabForSession(sid)
    if (existing) {
      focusExistingSessionTab(existing, sid, { fallbackTitle, explicitSelection: true, keepOpen: true })
      return
    }
    const id = `chat:${allocateTabNumber(bid, 'chat')}`
    openTab(
      { id, pinned: false, target: { kind: 'chat', sessionId: sid, explicitSelection: true, fallbackTitle } },
      { groupId: opts.groupId, activate: true, explicitOverride: true },
    )
  }

  function openSubagentSession(opts: { sessionId: string, title?: string }) {
    const bid = activeBotId()
    const sid = opts.sessionId.trim()
    if (!bid || !sid || isDeletedSessionForCurrentBot(sid)) return
    const fallbackTitle = opts.title?.trim() || undefined

    const existing = chatTabForSession(sid)
    if (existing) {
      focusExistingSessionTab(existing, sid, { fallbackTitle, explicitSelection: true, keepOpen: true })
      return
    }
    const primaryGroupId = findSpatialFirstGroupId(workbench.value.root)
    const primaryGroup = findGroup(workbench.value.root, primaryGroupId)
    if (isMobile.value || !primaryGroup || primaryGroup.tabIds.length === 0) {
      openSessionChatPinned({ sessionId: sid, title: opts.title })
      return
    }
    const id = `chat:${allocateTabNumber(bid, 'chat')}`
    const tab: WorkbenchTab = { id, pinned: false, target: { kind: 'chat', sessionId: sid, explicitSelection: true, fallbackTitle } }
    const rightNeighborId = findRightNeighborOfSpatialFirst(workbench.value.root)
    if (rightNeighborId) {
      openTab(tab, { groupId: rightNeighborId, activate: true, explicitOverride: true })
    } else {
      commitWorkbench(splitGroupWithTab(workbench.value, primaryGroupId, 'right', tab, { preview: false, activate: true }), true)
    }
  }

  function openDraftChat(opts?: { title?: string, groupId?: string, explicitSelection?: boolean }) {
    const bid = activeBotId()
    if (!bid) return
    focusChatInputAfterEntry()

    const candidateIds = opts?.groupId
      ? (findGroup(workbench.value.root, opts.groupId)?.tabIds ?? [])
      : Object.keys(workbench.value.tabs)
    const existingDraft = candidateIds
      .map(id => workbench.value.tabs[id])
      .find((t): t is WorkbenchTab => Boolean(t && t.target.kind === 'chat' && tabSessionId(t) === null))

    const explicitSelection = opts?.explicitSelection === true
    if (existingDraft) {
      if (activeId.value !== existingDraft.id) {
        commitWorkbench(treeActivateTab(workbench.value, existingDraft.id), explicitSelection)
      } else {
        chatStore.selectDraft({ explicitSelection })
      }
      return
    }

    const id = `chat:${allocateTabNumber(bid, 'chat')}`
    openTab(
      { id, pinned: false, target: { kind: 'chat', sessionId: null, explicitSelection } },
      { preview: true, groupId: opts?.groupId, activate: true, explicitOverride: explicitSelection },
    )
  }

  function alignSelectionWithDock() {
    if (!activeBotId()) return
    const sid = (chatStore.sessionId ?? '').trim()
    const explicitSelection = chatStore.hasExplicitSessionSelection === true
    if (sid && !explicitSelection && chatStore.loadingChats) return

    const active = activeTab.value
    if (!active) return
    if (active.target.kind !== 'chat') {
      if (sid && !chatTabForSession(sid)) {
        chatStore.resetToEmptyComposer({ clearPendingExternalAgent: false, explicitSelection: false, draftIntent: false })
      }
      return
    }

    chatStore.focusChatView(active.id)
    const activeSid = tabSessionId(active)
    if (activeSid) {
      if (activeSid !== sid) selectChatSession(activeSid, false)
      return
    }

    if (sid && explicitSelection && !isDeletedSessionForCurrentBot(sid) && !chatTabForSession(sid)) {
      commitWorkbench(updateTabTarget(workbench.value, active.id, () => ({ kind: 'chat', sessionId: sid, explicitSelection })))
      selectChatSession(sid, explicitSelection)
      return
    }

    if (active.target.explicitSelection !== explicitSelection) {
      commitWorkbench(updateTabTarget(workbench.value, active.id, t => (t.kind === 'chat' ? { ...t, explicitSelection } : t)))
    }
    chatStore.selectDraft({ explicitSelection })
  }

  function syncDraftChatExplicitSelection() {
    if (!activeBotId()) return
    const explicitSelection = !chatStore.sessionId && chatStore.hasExplicitSessionSelection === true
    const active = activeTab.value
    if (!active || active.target.kind !== 'chat' || tabSessionId(active) !== null) return
    if (active.target.explicitSelection === explicitSelection) return
    commitWorkbench(updateTabTarget(workbench.value, active.id, t => (t.kind === 'chat' ? { ...t, explicitSelection } : t)))
  }

  function reconcileDeletedChatPanels(targetBotId?: string | null) {
    if (!ready.value) return
    const bid = (targetBotId ?? loadedBotId.value ?? currentBotId.value ?? '').trim()
    if (!bid || !loadedBotId.value || loadedBotId.value !== bid) return
    const deletedSessionIds = deletedSessionIdsByBot.get(bid)
    if (!deletedSessionIds || deletedSessionIds.size === 0) return

    const latestDeleted = chatStore.deletedSession
    const resetComposerScope = latestDeleted?.botId === bid ? latestDeleted.composerScope?.trim() : ''
    let next = workbench.value
    let resetActiveToDraft = false

    for (const tab of Object.values(next.tabs)) {
      if (tab.target.kind !== 'chat') continue
      const sid = tabSessionId(tab)
      if (!sid || !deletedSessionIds.has(sid)) continue

      if (sid === latestDeleted?.id && resetComposerScope && resetComposerScope === `${bid}:${tab.id}`) {
        const explicitSelection = chatStore.hasExplicitSessionSelection === true
        next = updateTabTarget(next, tab.id, () => ({ kind: 'chat', sessionId: null, explicitSelection }))
        const owner = findGroupOfTab(next.root, tab.id)
        if (owner && !owner.previewTabId && !next.tabs[tab.id]?.pinned) {
          next = markPreviewTab(next, tab.id)
        }
        if (activeTabOfState(next)?.id === tab.id) resetActiveToDraft = true
        continue
      }
      next = removeTab(next, tab.id)
    }

    commitWorkbench(next)
    if (resetActiveToDraft && !suppressPersist) {
      chatStore.selectDraft({ explicitSelection: chatStore.hasExplicitSessionSelection === true })
    }
  }

  function activateChatPanel() {
    const bid = activeBotId()
    if (!bid) return
    const explicitSelection = chatStore.hasExplicitSessionSelection === true
    const sid = (selection.sessionId ?? '').trim()
    if (sid && !isDeletedSessionForCurrentBot(sid)) {
      const current = chatTabForSession(sid)
      if (current) {
        activateTab(current.id)
        return
      }
      if (explicitSelection) {
        openSessionChatPinned({ sessionId: sid })
        return
      }
    }
    const anyChat = Object.values(workbench.value.tabs).find(t => t.target.kind === 'chat')
    if (anyChat) {
      activateTab(anyChat.id)
      return
    }
    const id = `chat:${allocateTabNumber(bid, 'chat')}`
    openTab(
      { id, pinned: false, target: { kind: 'chat', sessionId: null, explicitSelection } },
      { explicitOverride: explicitSelection },
    )
  }

  // ---- File, Preview, Asset, Terminal, Browser, Display, Schedule ----------

  function openFile(filePath: string): boolean {
    if (!hasCurrentPermission('workspace_read')) return false
    const path = (filePath ?? '').trim()
    if (!path) return false
    return openTab({ id: `file:${path}`, pinned: false, target: { kind: 'file', filePath: path } }, { preview: true })
  }

  function openFilePinned(filePath: string, groupId?: string) {
    if (!hasCurrentPermission('workspace_read')) return
    const path = (filePath ?? '').trim()
    if (!path) return
    openTab({ id: `file:${path}`, pinned: false, target: { kind: 'file', filePath: path } }, { groupId, activate: true })
  }

  function openFileToSide(filePath: string, groupId?: string) {
    if (!hasCurrentPermission('workspace_read') || !activeBotId()) return
    const path = (filePath ?? '').trim()
    if (!path) return
    if (isMobile.value) {
      openFilePinned(path, groupId)
      return
    }
    const id = `file:${path}`
    const anchor = findGroup(workbench.value.root, groupId) ?? activeGroup.value ?? listGroups(workbench.value.root)[0]!
    if (workbench.value.tabs[id]) {
      let next = treeKeepOpenTab(workbench.value, id)
      const owner = findGroupOfTab(next.root, id)
      next = owner && owner.tabIds.length > 1
        ? moveTabToGroup(next, id, anchor.id, 'right')
        : treeActivateTab(next, id)
      commitWorkbench(next)
      return
    }
    commitWorkbench(splitGroupWithTab(
      workbench.value,
      anchor.id,
      'right',
      { id, pinned: false, target: { kind: 'file', filePath: path } },
      { preview: false, activate: true },
    ))
  }

  function openPreview(filePath: string, groupId?: string) {
    if (!hasCurrentPermission('workspace_read') || !activeBotId()) return
    const path = (filePath ?? '').trim()
    if (!path) return
    const id = `preview:${path}`
    if (workbench.value.tabs[id]) {
      activateTab(id)
      return
    }
    const existingEphemeralPreview = listGroups(workbench.value.root)
      .map(g => (g.previewTabId ? { group: g, tab: workbench.value.tabs[g.previewTabId] } : null))
      .find(item => item?.tab?.target.kind === 'preview')
    const tab: WorkbenchTab = { id, pinned: false, target: { kind: 'preview', filePath: path } }
    if (existingEphemeralPreview) {
      openTab(tab, { preview: true, groupId: existingEphemeralPreview.group.id, activate: true })
      return
    }
    if (isMobile.value) {
      openTab(tab, { preview: true, groupId, activate: true })
      return
    }
    commitWorkbench(splitGroupWithTab(workbench.value, groupId || activeGroup.value?.id, 'right', tab, { preview: true, activate: true }))
  }

  function openAsset(args: OpenAssetPreviewArgs) {
    const key = (args.key ?? '').trim()
    if (!key) return
    openTab(
      {
        id: `asset:${key}`,
        pinned: false,
        target: { kind: 'asset', assetKey: key, name: args.name || 'file', botId: args.botId, contentHash: args.contentHash, src: args.src },
      },
      { preview: true },
    )
  }

  function openTerminal(groupId?: string) {
    const bid = activeBotId()
    if (!bid || !hasCurrentPermission('workspace_exec')) return
    if (isMobile.value) {
      const existing = Object.values(workbench.value.tabs).find(t => t.target.kind === 'terminal')
      if (existing) {
        activateTab(existing.id)
        return
      }
    }
    const next = allocateTabNumber(bid, 'terminal')
    openTab({ id: `terminal:${next}`, pinned: false, target: { kind: 'terminal', instance: next } }, { groupId, activate: true })
  }

  function addBrowserPanel(address: string, customTitle: boolean, groupId?: string): boolean {
    const bid = activeBotId()
    if (!bid) return false
    const next = allocateTabNumber(bid, 'browser')
    return openTab(
      { id: `browser:${next}`, pinned: false, target: { kind: 'browser', instance: next, address, customTitle } },
      { groupId, activate: true },
    )
  }

  function openBrowser(groupId?: string) {
    if (!hasCurrentPermission('manage') || !activeBotId()) return
    if (isMobile.value) {
      const existing = Object.values(workbench.value.tabs).find(t => t.target.kind === 'browser')
      if (existing) {
        activateTab(existing.id)
        return
      }
    }
    addBrowserPanel(DEFAULT_BROWSER_ADDRESS, false, groupId)
  }

  function openBrowserAt(address: string, groupId?: string): boolean {
    if (!hasCurrentPermission('manage') || !activeBotId()) return false
    let target: string
    try {
      target = parseBrowserAddress(address).display
    } catch {
      return false
    }
    const existing = Object.values(workbench.value.tabs).find((tab) => {
      if (tab.target.kind !== 'browser' || !tab.target.address) return false
      try {
        return parseBrowserAddress(tab.target.address).display === target
      } catch {
        return false
      }
    })
    if (existing) {
      activateTab(existing.id)
      return true
    }
    return addBrowserPanel(target, true, groupId)
  }

  function openDisplay(groupId?: string) {
    if (!hasCurrentPermission('manage') || !activeBotId()) return
    const existing = Object.values(workbench.value.tabs).filter(t => t.target.kind === 'display')
    if (existing.length > 0) {
      let next = treeActivateTab(workbench.value, existing[0]!.id)
      for (const extra of existing.slice(1)) next = removeTab(next, extra.id)
      commitWorkbench(next)
      return
    }
    openTab({ id: DISPLAY_PANEL_ID, pinned: false, target: { kind: 'display' } }, { groupId, activate: true })
  }

  function openSchedule(scheduleId?: string, title?: string, groupId?: string) {
    const bid = activeBotId()
    if (!bid || !hasCurrentPermission('manage')) return
    const customTitle = title?.trim() || undefined
    if (scheduleId) {
      const panelId = `schedule:${scheduleId}`
      if (workbench.value.tabs[panelId]) {
        let next = updateTabTarget(workbench.value, panelId, t => (t.kind === 'schedule' ? { ...t, customTitle: customTitle ?? t.customTitle } : t))
        next = treeActivateTab(next, panelId)
        commitWorkbench(next)
        return
      }
      openTab({ id: panelId, pinned: false, target: { kind: 'schedule', scheduleId, customTitle } }, { groupId, activate: true })
      return
    }
    const panelId = `schedule:new:${allocateTabNumber(bid, 'schedule')}`
    openTab({ id: panelId, pinned: false, target: { kind: 'schedule', scheduleId: undefined, customTitle } }, { groupId, activate: true })
  }

  function uniqueSplitPanelId(baseId: string): string {
    const record = loadedBotId.value ? storage.value[loadedBotId.value] : undefined
    const exists = (id: string) => Boolean(workbench.value.tabs[id] || record?.workbench?.tabs?.[id])
    if (!exists(baseId)) return baseId
    let n = 2
    while (exists(`${baseId}~${n}`)) n += 1
    return `${baseId}~${n}`
  }

  function splitGroup(groupId: string, direction: 'right' | 'below') {
    const bid = activeBotId()
    if (!bid) return
    const group = findGroup(workbench.value.root, groupId)
    const source = group?.activeTabId ? workbench.value.tabs[group.activeTabId] : null
    if (!group || !source) return

    const splitDir: WorkbenchSplitDirection = direction === 'right' ? 'right' : 'bottom'
    const placeCopy = (copy: WorkbenchTab, preview = false, explicitOverride?: boolean) => {
      if (isMobile.value) {
        commitWorkbench(
          insertTab(workbench.value, copy, {
            groupId: group.id,
            preview,
            activate: true,
            canReplacePreview: id => !fileDirty.value[id],
          }),
          explicitOverride,
        )
      } else {
        commitWorkbench(splitGroupWithTab(workbench.value, group.id, splitDir, copy, { preview, activate: true }), explicitOverride)
      }
    }

    switch (source.target.kind) {
      case 'chat': {
        const id = `chat:${allocateTabNumber(bid, 'chat')}`
        placeCopy({ id, pinned: false, target: { ...source.target } }, true, source.target.explicitSelection)
        return
      }
      case 'terminal': {
        if (!hasCurrentPermission('workspace_exec')) return
        const next = allocateTabNumber(bid, 'terminal')
        placeCopy({ id: `terminal:${next}`, pinned: false, target: { kind: 'terminal', instance: next } })
        return
      }
      case 'browser': {
        if (!hasCurrentPermission('manage')) return
        const next = allocateTabNumber(bid, 'browser')
        placeCopy({ id: `browser:${next}`, pinned: false, target: { ...source.target, instance: next } })
        return
      }
      case 'display':
        if (hasCurrentPermission('manage')) openDisplay(group.id)
        return
      case 'file':
      case 'preview':
      case 'schedule':
      case 'asset': {
        if (!isTabAllowed(source)) return
        placeCopy({ id: uniqueSplitPanelId(source.id), pinned: false, target: { ...source.target } })
      }
    }
  }

  function updateTerminalTitle(panelId: string, title: unknown) {
    if (!activeBotId() || typeof title !== 'string') return
    const normalized = title.trim()
    if (!normalized) return
    commitWorkbench(updateTabTarget(workbench.value, panelId, t => (
      t.kind === 'terminal' && t.shellTitle !== normalized ? { ...t, shellTitle: normalized } : t
    )))
  }

  function updateBrowserAddress(panelId: string, address: string) {
    if (!activeBotId()) return
    commitWorkbench(updateTabTarget(workbench.value, panelId, (t) => {
      if (t.kind !== 'browser') return t
      const customTitle = address ? true : t.customTitle
      return t.address === address && t.customTitle === customTitle ? t : { ...t, address, customTitle }
    }))
  }

  function updateScheduleTitle(panelId: string, title: string) {
    if (!activeBotId()) return
    const nextCustom = title.trim() || undefined
    commitWorkbench(updateTabTarget(workbench.value, panelId, t => (
      t.kind === 'schedule' && t.customTitle !== nextCustom ? { ...t, customTitle: nextCustom } : t
    )))
  }

  // ---- Close & Unsaved-file confirmation queue -----------------------------

  function shouldKeepLastDraftChat(id: string, state: WorkbenchState = workbench.value): boolean {
    const ids = Object.keys(state.tabs)
    const tab = ids.length === 1 ? state.tabs[id] : undefined
    return Boolean(tab && tab.target.kind === 'chat' && tabSessionId(tab) === null)
  }

  function closeTab(id: string) {
    if (!ready.value || shouldKeepLastDraftChat(id)) return
    commitWorkbench(removeTab(workbench.value, id))
  }

  function setFileDirty(panelId: string, dirty: boolean, botId?: string) {
    const ownerBot = botId?.trim()
    if (ownerBot && ownerBot !== loadedBotId.value) return
    if (Boolean(fileDirty.value[panelId]) === dirty) return
    const next = { ...fileDirty.value }
    if (dirty) next[panelId] = true
    else delete next[panelId]
    fileDirty.value = next
    if (dirty && ready.value) keepOpenTab(panelId)
  }

  function registerFileSaveHandler(panelId: string, handler: () => Promise<boolean>, botId?: string) {
    saveHandlers.set(panelId, { botId: (botId ?? loadedBotId.value ?? '').trim(), handler })
  }

  function unregisterFileSaveHandler(panelId: string, handler?: () => Promise<boolean>) {
    const current = saveHandlers.get(panelId)
    if (current && (!handler || current.handler === handler)) saveHandlers.delete(panelId)
  }

  const dirtyFileCount = computed(() => Object.values(fileDirty.value).filter(Boolean).length)

  const pendingClose = computed<{ panelId: string, title: string } | null>(() => {
    const id = closeQueue.value[0]
    return id ? { panelId: id, title: resolveTabTitle(id) } : null
  })

  function requestCloseTab(id: string) {
    if (!ready.value || !workbench.value.tabs[id]) return
    if (!fileDirty.value[id]) {
      closeTab(id)
      return
    }
    if (!closeQueue.value.includes(id)) closeQueue.value = [...closeQueue.value, id]
  }

  function requestCloseTabs(ids: string[]) {
    if (!ready.value) return
    const toQueue: string[] = []
    let next = workbench.value
    for (const id of ids) {
      if (!next.tabs[id]) continue
      if (fileDirty.value[id]) {
        if (!toQueue.includes(id)) toQueue.push(id)
      } else if (!shouldKeepLastDraftChat(id, next)) {
        next = removeTab(next, id)
      }
    }
    commitWorkbench(next)
    if (toQueue.length > 0) {
      const merged = [...closeQueue.value]
      for (const id of toQueue) {
        if (workbench.value.tabs[id] && !merged.includes(id)) merged.push(id)
      }
      closeQueue.value = merged
    }
  }

  function closeOtherTabs(groupId: string, keepTabId: string) {
    const group = findGroup(workbench.value.root, groupId)
    if (group) requestCloseTabs(group.tabIds.filter(id => id !== keepTabId && !workbench.value.tabs[id]?.pinned))
  }

  function closeTabsToRight(groupId: string, fromTabId: string) {
    const group = findGroup(workbench.value.root, groupId)
    const idx = group ? group.tabIds.indexOf(fromTabId) : -1
    if (group && idx >= 0) requestCloseTabs(group.tabIds.slice(idx + 1).filter(id => !workbench.value.tabs[id]?.pinned))
  }

  function closeAllTabsInGroup(groupId: string) {
    const group = findGroup(workbench.value.root, groupId)
    if (group) requestCloseTabs(group.tabIds.filter(id => !workbench.value.tabs[id]?.pinned))
  }

  async function resolvePendingClose(action: 'save' | 'discard' | 'cancel') {
    const id = closeQueue.value[0]
    if (!id) return
    if (action === 'cancel') {
      closeQueue.value = []
      return
    }
    if (action === 'save') {
      const botAtStart = loadedBotId.value
      const entry = saveHandlers.get(id)
      if (entry && entry.botId && botAtStart && entry.botId !== botAtStart) {
        closeQueue.value = closeQueue.value.filter(q => q !== id)
        return
      }
      const ok = entry ? await entry.handler() : true
      if (!ready.value || loadedBotId.value !== botAtStart || closeQueue.value[0] !== id) return
      if (ok) closeTab(id)
    } else {
      closeTab(id)
    }
    closeQueue.value = closeQueue.value.filter(q => q !== id)
  }

  // ---- Sidebar & shell preferences -----------------------------------------

  const sidebarView = useLocalStorage<SidebarView>('workspace-sidebar-view', 'sessions')
  const sidebarWidth = useLocalStorage('workspace-sidebar-width', 256)
  const workbenchOpen = useLocalStorage('workspace-workbench-open', true)
  const mobileNavOpen = ref(false)
  const pendingFilesPath = ref<string | null>(null)

  function openMobileNav() { mobileNavOpen.value = true }
  function closeMobileNav() { mobileNavOpen.value = false }
  function toggleWorkbench() { workbenchOpen.value = !workbenchOpen.value }

  function selectSidebarView(view: SidebarView) {
    if (view === 'files' && !hasCurrentPermission('workspace_read')) return
    sidebarView.value = view
    workbenchOpen.value = true
  }

  function openFilesAt(path: string) {
    if (!hasCurrentPermission('workspace_read')) return
    workbenchOpen.value = true
    sidebarView.value = 'files'
    pendingFilesPath.value = (path ?? '').trim() || null
  }

  function consumePendingFilesPath(): string | null {
    const path = pendingFilesPath.value
    pendingFilesPath.value = null
    return path
  }

  // ---- Reset & global watchers ---------------------------------------------

  function resetBot(botId: string) {
    const bid = (botId ?? '').trim()
    if (!bid) return
    const next = { ...storage.value }
    delete next[bid]
    storage.value = next
    if (loadedBotId.value === bid && ready.value) clearRuntimeState()
    void nextTick(() => clearTerminalSnapshotsForBot(bid))
  }

  function resetAll() {
    storage.value = {}
    pendingFilesPath.value = null
    clearRuntimeState({ clearLoadedBot: true, clearMobileColdStart: true })
    void nextTick(() => clearTerminalSnapshots())
  }

  onAuthSessionCleared(() => resetAll())

  watch(currentBotId, (bid) => {
    const next = (bid ?? '').trim()
    if (!next) {
      clearRuntimeState({ clearLoadedBot: true, clearMobileColdStart: true })
      return
    }
    ensureBotRecord(next)
    if (ready.value && loadedBotId.value !== next) {
      restoreForBot(next)
    }
  }, { immediate: true })

  // ponytail: mobile cold-start avoids overwriting persisted desktop splits by staying in-memory until widen,
  // then appending non-duplicate mobile-opened tabs onto the saved desktop snapshot. Upgrade path: split mobile
  // vs desktop storage keys if users ever want independent phone layouts persisted across reloads.
  watch(isMobile, (mobile) => {
    if (mobile) {
      cancelActiveWorkbenchDrag()
      return
    }
    mobileNavOpen.value = false
    if (!mobileColdStart) {
      persistWorkbench()
      return
    }
    mobileColdStart = false
    const bid = loadedBotId.value
    if (!ready.value || !bid) return
    const parsed = parseWorkbenchState(storage.value[bid]?.workbench ?? null)
    if (!parsed) {
      persistWorkbench()
      return
    }
    const currentTabs = Object.values(workbench.value.tabs)
    const isPureColdStartDraft = currentTabs.length === 1 && currentTabs[0]?.target.kind === 'chat' && tabSessionId(currentTabs[0]) === null
    let merged = parsed
    let preferredActiveTabId: string | null = null
    if (!isPureColdStartDraft) {
      const currentActiveId = activeId.value
      for (const tab of currentTabs) {
        if (tab.target.kind === 'chat' && tabSessionId(tab) === null && currentActiveId !== tab.id) continue
        const existingMatch = merged.tabs[tab.id]
          ?? (tab.target.kind === 'chat' && tabSessionId(tab) ? chatTabForSession(tabSessionId(tab)!, merged) : undefined)
          ?? (tab.target.kind === 'display' ? Object.values(merged.tabs).find(t => t.target.kind === 'display') : undefined)
        if (existingMatch) {
          if (currentActiveId === tab.id) preferredActiveTabId = existingMatch.id
          continue
        }
        merged = insertTab(merged, tab, { preview: false, activate: currentActiveId === tab.id })
        if (currentActiveId === tab.id) preferredActiveTabId = tab.id
      }
      if (preferredActiveTabId && merged.tabs[preferredActiveTabId]) {
        merged = treeActivateTab(merged, preferredActiveTabId)
      }
    }
    suppressPersist = true
    try {
      workbench.value = merged
      pruneDisallowedTabs()
      reconcileDeletedChatPanels()
    } finally {
      suppressPersist = false
    }
    finalizeRestoredWorkbench(bid)
  })

  watch(
    () => [currentBotId.value, ...(currentBot.value?.current_user_permissions ?? [])].join('|'),
    () => pruneDisallowedTabs(),
  )

  watch(() => chatStore.draftPromoted, (sig) => {
    const bid = activeBotId()
    if (!sig || !bid || sig.botId !== bid) return
    const tab = workbench.value.tabs[sig.viewId]
    if (!tab || tab.target.kind !== 'chat' || tabSessionId(tab)) return
    commitWorkbench(updateTabTarget(workbench.value, tab.id, t => (t.kind === 'chat' ? { ...t, sessionId: sig.sessionId } : t)))
  }, { flush: 'sync' })

  watch(() => chatStore.userSentInSession, (sig) => {
    const bid = activeBotId()
    if (!sig || !bid || sig.botId !== bid) return
    const tab = workbench.value.tabs[sig.viewId]
    if (!tab || tab.target.kind !== 'chat') return
    const currentSid = tabSessionId(tab)
    let next = workbench.value
    if (sig.wasDraft) {
      if (currentSid && currentSid !== sig.id) return
      next = updateTabTarget(next, tab.id, t => (t.kind === 'chat' ? { ...t, sessionId: sig.id } : t))
    } else if (currentSid !== sig.id) {
      return
    }
    commitWorkbench(treeKeepOpenTab(next, tab.id))
  })

  watch(() => chatStore.draftViewRequested, (request) => {
    const bid = activeBotId()
    if (!request || !bid || request.botId !== bid) return
    const tab = workbench.value.tabs[request.viewId]
    const group = findGroupOfTab(workbench.value.root, request.viewId)
    if (!tab || !group || tab.target.kind !== 'chat' || tabSessionId(tab) !== request.expectedSessionId) return

    const activate = request.activate && activeId.value === tab.id
    if (group.previewTabId === tab.id) {
      chatStore.applyDraftViewRequest(request, activate)
      commitWorkbench(updateTabTarget(workbench.value, tab.id, () => ({
        kind: 'chat',
        sessionId: null,
        explicitSelection: request.explicitSelection,
      })))
      return
    }
    const id = `chat:${allocateTabNumber(bid, 'chat')}`
    if (!openTab(
      { id, pinned: false, target: { kind: 'chat', sessionId: null, explicitSelection: request.explicitSelection } },
      { preview: true, groupId: group.id, activate, explicitOverride: request.explicitSelection },
    )) return
    chatStore.applyDraftViewRequest({ ...request, viewId: id }, activeId.value === id)
  }, { flush: 'sync' })

  watch(() => chatStore.forkedSessionRequested, (request) => {
    const bid = activeBotId()
    if (!request || !bid || request.botId !== bid) return
    const tab = workbench.value.tabs[request.viewId]
    if (!tab || tab.target.kind !== 'chat' || tabSessionId(tab) !== request.expectedSessionId) return
    openSessionChatFromView({
      viewId: request.viewId,
      sessionId: request.sessionId,
      title: request.title,
      expectedSessionId: request.expectedSessionId,
      explicitSelection: request.explicitSelection,
      activate: request.activate && activeId.value === tab.id,
    })
  }, { flush: 'sync' })

  watch(
    () => `${chatStore.sessionId ?? ''}:${chatStore.hasExplicitSessionSelection === true ? '1' : '0'}`,
    () => syncDraftChatExplicitSelection(),
  )

  watch(() => chatStore.deletedSession, (deleted) => {
    if (!deleted) return
    const deletedIds = deletedSessionIdsByBot.get(deleted.botId) ?? new Set<string>()
    deletedIds.add(deleted.id)
    deletedSessionIdsByBot.set(deleted.botId, deletedIds)
    reconcileDeletedChatPanels(deleted.botId)
  })

  watch(() => chatStore.loadingChats, (loading) => {
    if (!loading) alignSelectionWithDock()
  })

  return {
    ready,
    loadedBotId,
    workbench,
    activeGroupId,
    activeId,
    spatialFirstGroupId,
    pendingChatInputFocus,
    requestChatInputFocus,
    focusAdjacentTab,
    isMobile,
    activePanelIsChat,
    mobileNavOpen,
    panelDragging,
    fileDirty,
    keepOpenTab,
    pinTab,
    unpinTab,
    dirtyFileCount,
    pendingClose,
    sidebarView,
    workbenchOpen,
    sidebarWidth,
    pendingFilesPath,
    mountWorkspace,
    unmountWorkspace,
    activateTab,
    activateGroup,
    setSplitRatio,
    reorderTab,
    moveTab,
    dropExternalItem,
    startExternalDrag,
    resolveTabTitle,
    selectSidebarView,
    toggleWorkbench,
    openMobileNav,
    closeMobileNav,
    activateChatPanel,
    openSessionChat,
    openSubagentSession,
    openSessionChatFromView,
    openSessionChatPinned,
    openDraftChat,
    openFile,
    openFilePinned,
    openFileToSide,
    openPreview,
    openAsset,
    openFilesAt,
    consumePendingFilesPath,
    openTerminal,
    openBrowser,
    openBrowserAt,
    openDisplay,
    splitGroup,
    openSchedule,
    closeTab,
    requestCloseTab,
    requestCloseTabs,
    closeOtherTabs,
    closeTabsToRight,
    closeAllTabsInGroup,
    resolvePendingClose,
    setFileDirty,
    registerFileSaveHandler,
    unregisterFileSaveHandler,
    updateBrowserAddress,
    updateTerminalTitle,
    updateScheduleTitle,
    resetBot,
  }
})
