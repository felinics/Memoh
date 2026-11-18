import { defineStore, storeToRefs } from 'pinia'
import { computed, nextTick, ref, shallowRef, watch } from 'vue'
import { useLocalStorage } from '@vueuse/core'
import { useTabScopedStorage } from '@/utils/tab-scoped-storage'
import { useIsMobile } from '@/composables/useIsMobile'
import { focusIsInTextInput } from '@/lib/keyboard-context'
import { useChatStore } from '@/store/chat-list'
import { useChatSelectionStore } from '@/store/chat-selection'
import { onAuthSessionCleared } from '@/lib/auth-session'
import { hasBotPermission, type BotPermission } from '@/utils/bot-permissions'
import { parseBrowserAddress } from '@/utils/browser-address'
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
  findAdjacentGroupId,
  findGroup,
  findGroupOfTab,
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
  splitRootWithTab,
  updateTabTarget,
  WORKBENCH_ROOT_DROP_ID,
} from '@/workbench/tree'
import { resolveWorkbenchTabTitle, workbenchFileBaseName } from '@/workbench/titles'
import type {
  WorkbenchDropZone,
  WorkbenchSplitDirection,
  WorkbenchState,
  WorkbenchTab,
  WorkbenchTabKind,
} from '@/workbench/types'

export type SidebarView = 'sessions' | 'files' | 'schedule' | 'supermarket'
export type WorkspacePanelComponent = WorkbenchTabKind

export const CHAT_PANEL_ID = 'chat'
export const DISPLAY_PANEL_ID = 'display:1'

const DEFAULT_BROWSER_ADDRESS = 'localhost:5173/'
const WORKBENCH_STORAGE_KEY = 'memoh:workbench:v1'
const LEGACY_STORAGE_KEYS = ['workspace-tabs', 'workspace-panes', 'workspace-layout'] as const

const ENTRY_OVERLAY = '[role="menu"], [role="menubar"], [role="dialog"], [role="alertdialog"], [role="listbox"]'
const ENTRY_OVERLAY_MAX_FRAMES = 120
const ENTRY_FOCUS_RETURN_MAX_FRAMES = 6

export interface BotWorkbenchRecord {
  workbench: WorkbenchState | null
  terminalCounter: number
  browserCounter: number
  scheduleCounter: number
  chatCounter: number
}

type WorkbenchStorageMap = Record<string, BotWorkbenchRecord>

function emptyBotRecord(): BotWorkbenchRecord {
  return {
    workbench: null,
    terminalCounter: 0,
    browserCounter: 0,
    scheduleCounter: 0,
    chatCounter: 0,
  }
}

export const useWorkspaceTabsStore = defineStore('workspace-tabs', () => {
  const selection = useChatSelectionStore()
  const { currentBotId } = storeToRefs(selection)
  const chatStore = useChatStore()
  const currentBot = computed(() =>
    chatStore.bots.find(bot => bot.id === currentBotId.value) ?? null,
  )
  const isMobile = useIsMobile()

  if (typeof localStorage !== 'undefined') {
    for (const key of LEGACY_STORAGE_KEYS) localStorage.removeItem(key)
  }
  if (typeof sessionStorage !== 'undefined') {
    for (const key of LEGACY_STORAGE_KEYS) sessionStorage.removeItem(key)
  }

  const storage = useTabScopedStorage<WorkbenchStorageMap>(WORKBENCH_STORAGE_KEY, {}, { seed: true })

  const ready = ref(false)
  const workbench = shallowRef<WorkbenchState>(createEmptyWorkbenchState())
  const panelDragging = ref(false)
  const fileDirty = ref<Record<string, boolean>>({})
  const saveHandlers = new Map<string, () => Promise<boolean>>()
  const closeQueue = ref<string[]>([])

  let loadedBotId: string | null = null
  let suppressPersist = false
  // Set when restore ran while mobile so phone cold-starts never overwrite a desktop snapshot.
  let mobileColdStart = false
  let draftChatQueued = false
  const deletedSessionIdsByBot = new Map<string, Set<string>>()
  const chatActivationExplicitOverrides = new Map<string, boolean>()

  function ensureBotRecord(botId: string | null | undefined): BotWorkbenchRecord | null {
    const bid = (botId ?? '').trim()
    if (!bid) return null
    const existing = storage.value[bid]
    if (!existing) {
      const fresh = emptyBotRecord()
      storage.value = { ...storage.value, [bid]: fresh }
      return fresh
    }
    if (
      typeof existing.terminalCounter !== 'number'
      || typeof existing.browserCounter !== 'number'
      || typeof existing.scheduleCounter !== 'number'
      || typeof existing.chatCounter !== 'number'
    ) {
      const normalized = { ...emptyBotRecord(), ...existing }
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

  const activeGroup = computed(() => {
    if (!ready.value) return null
    return findGroup(workbench.value.root, workbench.value.activeGroupId)
      ?? listGroups(workbench.value.root)[0]
      ?? null
  })

  const activeGroupId = computed<string | null>(() => activeGroup.value?.id ?? null)

  const activeId = computed<string | null>(() => {
    if (!ready.value) return null
    return activeGroup.value?.activeTabId ?? null
  })

  const activeTab = computed<WorkbenchTab | null>(() => {
    const id = activeId.value
    return id ? workbench.value.tabs[id] ?? null : null
  })

  const spatialFirstGroupId = computed<string | null>(() => {
    if (!ready.value) return null
    return findSpatialFirstGroupId(workbench.value.root)
  })

  const activePanelIsChat = computed(() => {
    const tab = activeTab.value
    return !tab || tab.target.kind === 'chat'
  })

  let pendingDragPersist = false

  function persistWorkbench() {
    if (!ready.value || suppressPersist || !loadedBotId || isMobile.value || mobileColdStart) return
    if (panelDragging.value) {
      pendingDragPersist = true
      return
    }
    pendingDragPersist = false
    patchBotRecord(loadedBotId, { workbench: workbench.value })
  }

  watch(panelDragging, (dragging) => {
    if (!dragging && pendingDragPersist) {
      pendingDragPersist = false
      persistWorkbench()
    }
  })

  function setNextChatActivationExplicit(panelId: string, explicitSelection: boolean) {
    chatActivationExplicitOverrides.set(panelId, explicitSelection)
  }

  function consumeNextChatActivationExplicit(panelId: string): boolean | null {
    if (!chatActivationExplicitOverrides.has(panelId)) return null
    const explicitSelection = chatActivationExplicitOverrides.get(panelId) === true
    chatActivationExplicitOverrides.delete(panelId)
    return explicitSelection
  }

  function selectChatSession(sid: string, explicitSelection: boolean) {
    if (explicitSelection) {
      void chatStore.selectSession(sid)
    } else {
      void chatStore.selectSession(sid, { explicitSelection: false })
    }
  }

  function tabSessionId(tab: WorkbenchTab | undefined | null): string | null {
    if (!tab || tab.target.kind !== 'chat') return null
    const sid = tab.target.sessionId?.trim()
    return sid || null
  }

  function chatTabForSession(sid: string, state: WorkbenchState = workbench.value): WorkbenchTab | undefined {
    return Object.values(state.tabs).find(
      tab => tab.target.kind === 'chat' && tabSessionId(tab) === sid,
    )
  }

  function isDeletedSessionForCurrentBot(sid: string | null): boolean {
    const bid = (currentBotId.value ?? '').trim()
    if (!bid || !sid) return false
    const latestDeleted = chatStore.deletedSession
    if (latestDeleted?.botId === bid && latestDeleted.id === sid) return true
    return Boolean(deletedSessionIdsByBot.get(bid)?.has(sid))
  }

  function activateChatSession(tab: WorkbenchTab, options?: { explicitSelection?: boolean }) {
    if (suppressPersist || tab.target.kind !== 'chat') return
    const deletedSid = tabSessionId(tab)
    if (deletedSid && isDeletedSessionForCurrentBot(deletedSid)) return
    chatStore.focusChatView(tab.id)
    const explicitOverride = consumeNextChatActivationExplicit(tab.id)
    const sid = tabSessionId(tab)
    if (sid) {
      const explicitSelection = options?.explicitSelection
        ?? explicitOverride
        ?? tab.target.explicitSelection
      selectChatSession(sid, explicitSelection)
    } else {
      const explicitSelection = options?.explicitSelection
        ?? explicitOverride
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
    const id = group?.activeTabId
    return id ? state.tabs[id] ?? null : null
  }

  /**
   * Single state-commit funnel: reconciles removed-tab side effects, terminal cache
   * cleanup, chat selection mirroring, empty-workspace draft refill, and persistence.
   */
  function commitWorkbench(nextState: WorkbenchState) {
    const prevState = workbench.value
    if (nextState === prevState) return

    const prevActive = activeTabOfState(prevState)
    const removedTabs: WorkbenchTab[] = []
    for (const [id, tab] of Object.entries(prevState.tabs)) {
      if (!nextState.tabs[id]) removedTabs.push(tab)
    }

    workbench.value = nextState
    const nextActive = activeTabOfState(nextState)

    for (const removed of removedTabs) {
      cleanupPanelState(removed.id)
      if (removed.target.kind === 'terminal' && loadedBotId) {
        const bid = loadedBotId
        void nextTick(() => deleteTerminalSnapshot(terminalCacheKey(bid, removed.id)))
      }
    }

    const hadRemovedChat = removedTabs.some(t => t.target.kind === 'chat')
    if (hadRemovedChat) {
      const anyChatRemaining = Object.values(nextState.tabs).some(t => t.target.kind === 'chat')
      if (!anyChatRemaining) {
        chatStore.selectDraft({ explicitSelection: false })
      } else if (!suppressPersist) {
        const sid = (chatStore.sessionId ?? '').trim()
        const closedSelectedSid = removedTabs.some(t => tabSessionId(t) === sid)
        if (
          sid
          && closedSelectedSid
          && !chatTabForSession(sid, nextState)
          && (!nextActive || nextActive.target.kind !== 'chat')
        ) {
          chatStore.resetToEmptyComposer({
            clearPendingExternalAgent: false,
            explicitSelection: false,
            draftIntent: false,
          })
        }
      }
    }

    if (nextActive && nextActive.id !== prevActive?.id && nextActive.target.kind === 'chat') {
      activateChatSession(nextActive)
    }

    persistWorkbench()

    if (Object.keys(nextState.tabs).length === 0) {
      ensureDraftChatPanel()
    }
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
    if (!ready.value) return
    const perms = currentBot.value?.current_user_permissions
    if (!perms || perms.length === 0) return
    let next = workbench.value
    for (const tab of Object.values(next.tabs)) {
      if (!isTabAllowed(tab)) {
        next = removeTab(next, tab.id)
      }
    }
    commitWorkbench(next)
  }

  function restoreForBot(botId: string) {
    if (!ready.value) return
    let emptyAfterRestore = false
    suppressPersist = true
    try {
      const stored = storage.value[botId]?.workbench ?? null
      if (stored && !isMobile.value) {
        const parsed = parseWorkbenchState(stored)
        if (parsed) {
          workbench.value = parsed
        } else {
          workbench.value = createEmptyWorkbenchState()
          patchBotRecord(botId, { workbench: null })
        }
      } else {
        workbench.value = createEmptyWorkbenchState()
      }
      loadedBotId = botId
      mobileColdStart = isMobile.value
      fileDirty.value = {}
      closeQueue.value = []
      pruneDisallowedTabs()
      emptyAfterRestore = Object.keys(workbench.value.tabs).length === 0
    } finally {
      suppressPersist = false
    }
    if (!emptyAfterRestore) persistWorkbench()
    if (emptyAfterRestore) ensureDraftChatPanel()
    syncDraftChatExplicitSelection()
    alignSelectionWithDock()
    focusChatInputAfterEntry()
  }

  function mountWorkspace() {
    ready.value = true
    const bid = (currentBotId.value ?? '').trim()
    if (bid) {
      ensureBotRecord(bid)
      restoreForBot(bid)
      reconcileDeletedChatPanels()
    }
  }

  function unmountWorkspace() {
    ready.value = false
    loadedBotId = null
    panelDragging.value = false
    draftChatQueued = false
    workbench.value = createEmptyWorkbenchState()
  }

  // ---- Focus management ----------------------------------------------------

  const pendingChatInputFocus = ref<{ panelId: string; botId: string; sessionId: string | null } | null>(null)
  watch([activeId, currentBotId, () => selection.sessionId], () => { pendingChatInputFocus.value = null }, { flush: 'sync' })

  function requestChatInputFocus() {
    if (!activeTab.value || activeTab.value.target.kind !== 'chat') activateChatPanel()
    requestActiveChatInputFocus()
  }

  function requestActiveChatInputFocus() {
    const tab = activeTab.value
    if (!tab || tab.target.kind !== 'chat' || !currentBotId.value) return
    pendingChatInputFocus.value = {
      panelId: tab.id,
      botId: currentBotId.value,
      sessionId: tab.target.sessionId,
    }
  }

  let entryFocusRun = 0
  function focusChatInputAfterEntry() {
    if (isMobile.value) return
    const run = ++entryFocusRun
    void nextTick(() => {
      if (isMobile.value || run !== entryFocusRun) return
      const overlay = document.activeElement?.closest<HTMLElement>(ENTRY_OVERLAY)
      if (overlay) {
        afterOverlayReleasesFocus(overlay, run)
        return
      }
      if (focusIsInTextInput()) return
      requestActiveChatInputFocus()
    })
  }

  function afterOverlayReleasesFocus(
    overlay: HTMLElement,
    run: number,
    framesLeft = ENTRY_OVERLAY_MAX_FRAMES,
    returnFramesLeft = ENTRY_FOCUS_RETURN_MAX_FRAMES,
  ) {
    requestAnimationFrame(() => {
      if (run !== entryFocusRun || isMobile.value) return
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
    const group = activeGroup.value
    if (!group || group.tabIds.length < 2) return false
    const idx = group.tabIds.indexOf(group.activeTabId ?? '')
    if (idx < 0) return false
    const nextId = group.tabIds[(idx + direction + group.tabIds.length) % group.tabIds.length]
    if (!nextId) return false
    activateTab(nextId)
    return true
  }

  function isPreviewTab(tabId: string): boolean {
    const group = findGroupOfTab(workbench.value.root, tabId)
    return group?.previewTabId === tabId
  }

  function keepOpenTab(tabId: string) {
    if (!ready.value) return
    commitWorkbench(treeKeepOpenTab(workbench.value, tabId))
  }

  // Retained method name for callers that promote a preview tab on first edit/send.
  function pinPanel(tabId: string) {
    keepOpenTab(tabId)
  }

  function pinTab(tabId: string) {
    if (!ready.value) return
    commitWorkbench(setTabPinned(workbench.value, tabId, true))
  }

  function unpinTab(tabId: string) {
    if (!ready.value) return
    commitWorkbench(setTabPinned(workbench.value, tabId, false))
  }

  function togglePinTab(tabId: string) {
    if (!ready.value) return
    const tab = workbench.value.tabs[tabId]
    if (!tab) return
    commitWorkbench(setTabPinned(workbench.value, tabId, !tab.pinned))
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

  function dropExternalItem(
    item: { kind: 'session', sessionId: string, title?: string } | { kind: 'file', filePath: string },
    targetGroupId: string,
    zone: WorkbenchDropZone,
    insertIndex?: number,
  ) {
    if (!ready.value) return
    const effectiveZone: WorkbenchDropZone = isMobile.value && zone !== 'center' ? 'center' : zone

    if (item.kind === 'session') {
      const sid = item.sessionId.trim()
      const bid = (currentBotId.value ?? '').trim()
      if (!sid || !bid || isDeletedSessionForCurrentBot(sid)) return
      focusChatInputAfterEntry()

      const existing = chatTabForSession(sid)
      if (existing && existing.target.kind === 'chat') {
        const fallbackTitle = item.title?.trim() || undefined
        let next = updateTabTarget(workbench.value, existing.id, t => t.kind === 'chat'
          ? { ...t, explicitSelection: true, fallbackTitle: fallbackTitle ?? t.fallbackTitle }
          : t)
        next = treeKeepOpenTab(next, existing.id)
        setNextChatActivationExplicit(existing.id, true)
        next = moveTabToGroup(next, existing.id, targetGroupId, effectiveZone, insertIndex)
        commitWorkbench(next)
        selectChatSession(sid, true)
        return
      }

      const id = nextChatPanelId(bid)
      const tab: WorkbenchTab = {
        id,
        pinned: false,
        target: {
          kind: 'chat',
          sessionId: sid,
          explicitSelection: true,
          fallbackTitle: item.title?.trim() || undefined,
        },
      }
      setNextChatActivationExplicit(id, true)
      if (targetGroupId === WORKBENCH_ROOT_DROP_ID && effectiveZone !== 'center') {
        commitWorkbench(splitRootWithTab(workbench.value, effectiveZone, tab, { preview: false, activate: true }))
      } else if (effectiveZone !== 'center') {
        commitWorkbench(splitGroupWithTab(workbench.value, targetGroupId, effectiveZone, tab, { preview: false, activate: true }))
      } else {
        let next = insertTab(workbench.value, tab, { groupId: targetGroupId, preview: false, activate: true }).state
        if (typeof insertIndex === 'number') {
          next = reorderTabInGroup(next, targetGroupId, id, insertIndex)
        }
        commitWorkbench(next)
      }
      return
    }

    if (!hasCurrentPermission('workspace_read')) return
    const path = item.filePath.trim()
    if (!path) return
    const id = `file:${path}`
    if (workbench.value.tabs[id]) {
      let next = treeKeepOpenTab(workbench.value, id)
      next = moveTabToGroup(next, id, targetGroupId, effectiveZone, insertIndex)
      commitWorkbench(next)
      return
    }

    const tab: WorkbenchTab = { id, pinned: false, target: { kind: 'file', filePath: path } }
    if (targetGroupId === WORKBENCH_ROOT_DROP_ID && effectiveZone !== 'center') {
      commitWorkbench(splitRootWithTab(workbench.value, effectiveZone, tab, { preview: false, activate: true }))
    } else if (effectiveZone !== 'center') {
      commitWorkbench(splitGroupWithTab(workbench.value, targetGroupId, effectiveZone, tab, { preview: false, activate: true }))
    } else {
      let next = insertTab(workbench.value, tab, { groupId: targetGroupId, preview: false, activate: true }).state
      if (typeof insertIndex === 'number') {
        next = reorderTabInGroup(next, targetGroupId, id, insertIndex)
      }
      commitWorkbench(next)
    }
  }

  function resolveTabTitle(tabId: string): string {
    const tab = workbench.value.tabs[tabId]
    if (!tab) return tabId
    return resolveWorkbenchTabTitle(tab.target, chatStore)
  }

  const ephemeralPanels = computed<Record<string, true>>(() => {
    const out: Record<string, true> = {}
    for (const g of listGroups(workbench.value.root)) {
      if (g.previewTabId) out[g.previewTabId] = true
    }
    return out
  })

  // ---- Chat & Session operations -------------------------------------------

  function nextChatPanelId(bid: string): string {
    const record = ensureBotRecord(bid)
    const next = (record?.chatCounter ?? 0) + 1
    patchBotRecord(bid, { chatCounter: next })
    return `chat:${next}`
  }

  function openEphemeralTab(
    tab: WorkbenchTab,
    opts?: { groupId?: string, activate?: boolean },
  ): boolean {
    if (!ready.value) return false
    const existing = workbench.value.tabs[tab.id]
    if (existing) {
      if (opts?.activate !== false) activateTab(existing.id)
      return true
    }
    const { state } = insertTab(workbench.value, tab, {
      groupId: opts?.groupId,
      preview: true,
      activate: opts?.activate,
      canReplacePreview: id => !fileDirty.value[id],
    })
    commitWorkbench(state)
    return true
  }

  function openRegularTab(
    tab: WorkbenchTab,
    opts?: { groupId?: string, activate?: boolean },
  ): boolean {
    if (!ready.value) return false
    const existing = workbench.value.tabs[tab.id]
    if (existing) {
      let next = treeKeepOpenTab(workbench.value, existing.id)
      if (opts?.activate !== false) next = treeActivateTab(next, existing.id)
      commitWorkbench(next)
      return true
    }
    const { state } = insertTab(workbench.value, tab, {
      groupId: opts?.groupId,
      preview: false,
      activate: opts?.activate,
    })
    commitWorkbench(state)
    return true
  }

  function openSessionChat(opts: {
    sessionId: string
    title?: string
    groupId?: string
    explicitSelection?: boolean
    groupLocal?: boolean
    activate?: boolean
  }) {
    if (!ready.value) return
    const sid = opts.sessionId.trim()
    if (!sid) return
    const bid = (currentBotId.value ?? '').trim()
    if (!bid || isDeletedSessionForCurrentBot(sid)) return

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
      let next = updateTabTarget(workbench.value, existing.id, t => t.kind === 'chat'
        ? { ...t, explicitSelection, fallbackTitle: fallbackTitle ?? t.fallbackTitle }
        : t)
      if (!activate) {
        commitWorkbench(next)
        return
      }
      if (activeId.value === existing.id) {
        commitWorkbench(next)
        selectChatSession(sid, explicitSelection)
        return
      }
      setNextChatActivationExplicit(existing.id, explicitSelection)
      next = treeActivateTab(next, existing.id)
      commitWorkbench(next)
      return
    }

    const targetGroup = findGroup(workbench.value.root, opts.groupId) ?? activeGroup.value
    const reusableId = targetGroup?.previewTabId ?? null
    const reusableTab = reusableId ? workbench.value.tabs[reusableId] : undefined
    if (
      reusableTab
      && reusableTab.target.kind === 'chat'
      && !isDeletedSessionForCurrentBot(tabSessionId(reusableTab))
    ) {
      let next = updateTabTarget(workbench.value, reusableTab.id, () => ({
        kind: 'chat',
        sessionId: sid,
        explicitSelection,
        fallbackTitle,
      }))
      if (!activate) {
        commitWorkbench(next)
        return
      }
      const wasAlreadyActive = activeId.value === reusableTab.id
      setNextChatActivationExplicit(reusableTab.id, explicitSelection)
      next = treeActivateTab(next, reusableTab.id)
      commitWorkbench(next)
      if (wasAlreadyActive) {
        consumeNextChatActivationExplicit(reusableTab.id)
        chatStore.focusChatView(reusableTab.id)
        selectChatSession(sid, explicitSelection)
      }
      return
    }

    const id = nextChatPanelId(bid)
    if (activate) setNextChatActivationExplicit(id, explicitSelection)
    openEphemeralTab(
      {
        id,
        pinned: false,
        target: { kind: 'chat', sessionId: sid, explicitSelection, fallbackTitle },
      },
      { groupId: opts.groupId, activate },
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
    if (!ready.value) return
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
    if (!ready.value) return
    const sid = opts.sessionId.trim()
    if (!sid) return
    const bid = (currentBotId.value ?? '').trim()
    if (!bid || isDeletedSessionForCurrentBot(sid)) return
    const fallbackTitle = opts.title?.trim() || undefined
    focusChatInputAfterEntry()

    const existing = chatTabForSession(sid)
    if (existing) {
      let next = updateTabTarget(workbench.value, existing.id, t => t.kind === 'chat'
        ? { ...t, fallbackTitle: fallbackTitle ?? t.fallbackTitle }
        : t)
      next = treeKeepOpenTab(next, existing.id)
      next = treeActivateTab(next, existing.id)
      commitWorkbench(next)
      return
    }

    const id = nextChatPanelId(bid)
    openRegularTab(
      {
        id,
        pinned: false,
        target: { kind: 'chat', sessionId: sid, explicitSelection: true, fallbackTitle },
      },
      { groupId: opts.groupId, activate: true },
    )
  }

  function openSubagentSession(opts: { sessionId: string, title?: string }) {
    if (!ready.value) return
    const sid = opts.sessionId.trim()
    if (!sid) return
    const bid = (currentBotId.value ?? '').trim()
    if (!bid || isDeletedSessionForCurrentBot(sid)) return
    const fallbackTitle = opts.title?.trim() || undefined

    const existing = chatTabForSession(sid)
    if (existing) {
      let next = updateTabTarget(workbench.value, existing.id, t => t.kind === 'chat'
        ? { ...t, fallbackTitle: fallbackTitle ?? t.fallbackTitle }
        : t)
      next = treeKeepOpenTab(next, existing.id)
      next = treeActivateTab(next, existing.id)
      commitWorkbench(next)
      return
    }

    if (isMobile.value) {
      openSessionChatPinned({ sessionId: sid, title: opts.title })
      return
    }

    const primaryGroupId = findSpatialFirstGroupId(workbench.value.root)
    const primaryGroup = findGroup(workbench.value.root, primaryGroupId)
    if (!primaryGroup || primaryGroup.tabIds.length === 0) {
      openSessionChatPinned({ sessionId: sid, title: opts.title })
      return
    }

    const id = nextChatPanelId(bid)
    const tab: WorkbenchTab = {
      id,
      pinned: false,
      target: { kind: 'chat', sessionId: sid, explicitSelection: true, fallbackTitle },
    }
    const rightNeighborId = findAdjacentGroupId(workbench.value.root, primaryGroupId, 'right')
    if (rightNeighborId) {
      openRegularTab(tab, { groupId: rightNeighborId, activate: true })
    } else {
      commitWorkbench(splitGroupWithTab(workbench.value, primaryGroupId, 'right', tab, { preview: false, activate: true }))
    }
  }

  function openDraftChat(opts?: { title?: string, groupId?: string, explicitSelection?: boolean }) {
    if (!ready.value) return
    const bid = (currentBotId.value ?? '').trim()
    if (!bid) return
    focusChatInputAfterEntry()

    const candidateIds = opts?.groupId
      ? findGroup(workbench.value.root, opts.groupId)?.tabIds ?? []
      : Object.keys(workbench.value.tabs)
    const existingDraft = candidateIds
      .map(id => workbench.value.tabs[id])
      .find((t): t is WorkbenchTab => Boolean(t && t.target.kind === 'chat' && tabSessionId(t) === null))

    if (existingDraft) {
      const explicitSelection = opts?.explicitSelection === true
      if (activeId.value !== existingDraft.id) {
        setNextChatActivationExplicit(existingDraft.id, explicitSelection)
        activateTab(existingDraft.id)
      }
      chatStore.selectDraft({ explicitSelection })
      return
    }

    const id = nextChatPanelId(bid)
    const explicitSelection = opts?.explicitSelection === true
    setNextChatActivationExplicit(id, explicitSelection)
    openEphemeralTab(
      {
        id,
        pinned: false,
        target: { kind: 'chat', sessionId: null, explicitSelection },
      },
      { groupId: opts?.groupId, activate: true },
    )
  }

  function alignSelectionWithDock() {
    if (!ready.value) return
    const sid = (chatStore.sessionId ?? '').trim()
    const explicitSelection = chatStore.hasExplicitSessionSelection === true
    if (sid && !explicitSelection && chatStore.loadingChats) return

    const active = activeTab.value
    if (!active) return
    if (active.target.kind !== 'chat') {
      if (sid && !chatTabForSession(sid)) {
        chatStore.resetToEmptyComposer({
          clearPendingExternalAgent: false,
          explicitSelection: false,
          draftIntent: false,
        })
      }
      return
    }

    chatStore.focusChatView(active.id)
    const activeSid = tabSessionId(active)
    if (activeSid) {
      if (activeSid === sid) return
      selectChatSession(activeSid, false)
      return
    }

    if (
      sid
      && explicitSelection
      && !isDeletedSessionForCurrentBot(sid)
      && !chatTabForSession(sid)
    ) {
      commitWorkbench(updateTabTarget(workbench.value, active.id, () => ({
        kind: 'chat',
        sessionId: sid,
        explicitSelection,
      })))
      selectChatSession(sid, explicitSelection)
      return
    }

    if (active.target.explicitSelection !== explicitSelection) {
      commitWorkbench(updateTabTarget(workbench.value, active.id, t => t.kind === 'chat'
        ? { ...t, explicitSelection }
        : t))
    }
    chatStore.selectDraft({ explicitSelection })
  }

  function syncDraftChatExplicitSelection() {
    if (!ready.value) return
    const explicitSelection = !chatStore.sessionId && chatStore.hasExplicitSessionSelection === true
    const active = activeTab.value
    if (!active || active.target.kind !== 'chat' || tabSessionId(active) !== null) return
    if (active.target.explicitSelection === explicitSelection) return
    commitWorkbench(updateTabTarget(workbench.value, active.id, t => t.kind === 'chat'
      ? { ...t, explicitSelection }
      : t))
  }

  function reconcileDeletedChatPanels(targetBotId?: string | null) {
    if (!ready.value) return
    const bid = (targetBotId ?? loadedBotId ?? currentBotId.value ?? '').trim()
    if (!bid) return
    if (!targetBotId && loadedBotId && loadedBotId !== bid) return
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

      if (resetComposerScope && resetComposerScope === `${bid}:${tab.id}`) {
        const explicitSelection = chatStore.hasExplicitSessionSelection === true
        next = updateTabTarget(next, tab.id, () => ({
          kind: 'chat',
          sessionId: null,
          explicitSelection,
        }))
        const owner = findGroupOfTab(next.root, tab.id)
        if (owner && !owner.previewTabId && !next.tabs[tab.id]?.pinned) {
          next = markPreviewTab(next, tab.id)
        }
        if (activeTabOfState(next)?.id === tab.id) {
          resetActiveToDraft = true
        }
        continue
      }
      next = removeTab(next, tab.id)
    }

    commitWorkbench(next)
    if (resetActiveToDraft) {
      chatStore.selectDraft({ explicitSelection: chatStore.hasExplicitSessionSelection === true })
    }
  }

  function ensureDraftChatPanel() {
    if (!ready.value || suppressPersist || draftChatQueued) return
    if (Object.keys(workbench.value.tabs).length > 0) return
    if (!(currentBotId.value ?? '').trim()) return

    draftChatQueued = true
    void nextTick(() => {
      draftChatQueued = false
      if (!ready.value || suppressPersist || Object.keys(workbench.value.tabs).length > 0) return
      if (!(currentBotId.value ?? '').trim()) return
      const sid = (chatStore.sessionId ?? '').trim()
      const explicitSelection = chatStore.hasExplicitSessionSelection === true
      if (sid && explicitSelection && !isDeletedSessionForCurrentBot(sid)) {
        openSessionChat({ sessionId: sid, explicitSelection })
      } else {
        openDraftChat({ explicitSelection })
      }
    })
  }

  function activateChatPanel() {
    const bid = (currentBotId.value ?? '').trim()
    if (!ready.value || !bid || loadedBotId !== bid) return
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
    const id = nextChatPanelId(bid)
    setNextChatActivationExplicit(id, explicitSelection)
    openRegularTab({
      id,
      pinned: false,
      target: { kind: 'chat', sessionId: null, explicitSelection },
    })
  }

  // ---- File, Preview, Asset, Terminal, Browser, Display, Schedule ----------

  function openFile(filePath: string): boolean {
    if (!hasCurrentPermission('workspace_read') || !ready.value) return false
    const path = (filePath ?? '').trim()
    if (!path) return false
    return openEphemeralTab({
      id: `file:${path}`,
      pinned: false,
      target: { kind: 'file', filePath: path },
    })
  }

  function openFilePinned(filePath: string, groupId?: string) {
    if (!hasCurrentPermission('workspace_read') || !ready.value) return
    const path = (filePath ?? '').trim()
    if (!path) return
    openRegularTab(
      {
        id: `file:${path}`,
        pinned: false,
        target: { kind: 'file', filePath: path },
      },
      { groupId, activate: true },
    )
  }

  function openFileToSide(filePath: string, groupId?: string) {
    if (!hasCurrentPermission('workspace_read') || !ready.value) return
    const path = (filePath ?? '').trim()
    if (!path) return
    if (isMobile.value) {
      openFilePinned(path, groupId)
      return
    }
    const id = `file:${path}`
    const anchor = findGroup(workbench.value.root, groupId) ?? activeGroup.value ?? listGroups(workbench.value.root)[0]!
    const existing = workbench.value.tabs[id]
    if (existing) {
      let next = treeKeepOpenTab(workbench.value, id)
      const owner = findGroupOfTab(next.root, id)
      if (owner && owner.tabIds.length > 1) {
        next = moveTabToGroup(next, id, anchor.id, 'right')
      } else {
        next = treeActivateTab(next, id)
      }
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

  function openPreview(filePath: string, _title?: string, groupId?: string) {
    if (!hasCurrentPermission('workspace_read') || !ready.value) return
    const path = (filePath ?? '').trim()
    if (!path) return
    const id = `preview:${path}`
    if (workbench.value.tabs[id]) {
      activateTab(id)
      return
    }
    const existingEphemeralPreview = listGroups(workbench.value.root)
      .map(g => g.previewTabId ? { group: g, tab: workbench.value.tabs[g.previewTabId] } : null)
      .find(item => item?.tab?.target.kind === 'preview')

    const tab: WorkbenchTab = {
      id,
      pinned: false,
      target: { kind: 'preview', filePath: path },
    }
    if (existingEphemeralPreview) {
      openEphemeralTab(tab, { groupId: existingEphemeralPreview.group.id, activate: true })
      return
    }
    if (isMobile.value) {
      openEphemeralTab(tab, { groupId, activate: true })
      return
    }
    const anchorId = groupId || activeGroup.value?.id
    commitWorkbench(splitGroupWithTab(workbench.value, anchorId, 'right', tab, { preview: true, activate: true }))
  }

  function openAsset(args: OpenAssetPreviewArgs) {
    if (!ready.value) return
    const key = (args.key ?? '').trim()
    if (!key) return
    openEphemeralTab({
      id: `asset:${key}`,
      pinned: false,
      target: {
        kind: 'asset',
        assetKey: key,
        name: args.name || 'file',
        botId: args.botId,
        contentHash: args.contentHash,
        src: args.src,
      },
    })
  }

  function nextTabNumber(kind: 'terminal' | 'browser', counter: number): number {
    const anyOpen = Object.values(workbench.value.tabs).some(t => t.target.kind === kind)
    return anyOpen ? counter + 1 : 1
  }

  function openTerminal(groupId?: string) {
    if (!hasCurrentPermission('workspace_exec') || !ready.value) return
    const bid = (currentBotId.value ?? '').trim()
    const record = ensureBotRecord(bid)
    if (!record) return
    if (isMobile.value) {
      const existing = Object.values(workbench.value.tabs).find(t => t.target.kind === 'terminal')
      if (existing) {
        activateTab(existing.id)
        return
      }
    }
    const next = nextTabNumber('terminal', record.terminalCounter)
    patchBotRecord(bid, { terminalCounter: next })
    openRegularTab(
      {
        id: `terminal:${next}`,
        pinned: false,
        target: { kind: 'terminal', instance: next },
      },
      { groupId, activate: true },
    )
  }

  function addBrowserPanel(address: string, customTitle: boolean, groupId?: string): boolean {
    const bid = (currentBotId.value ?? '').trim()
    const record = ensureBotRecord(bid)
    if (!record || !ready.value) return false
    const next = nextTabNumber('browser', record.browserCounter)
    patchBotRecord(bid, { browserCounter: next })
    return openRegularTab(
      {
        id: `browser:${next}`,
        pinned: false,
        target: { kind: 'browser', instance: next, address, customTitle },
      },
      { groupId, activate: true },
    )
  }

  function openBrowser(groupId?: string) {
    if (!hasCurrentPermission('manage') || !ready.value) return
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
    if (!hasCurrentPermission('manage') || !ready.value) return false
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
    if (!hasCurrentPermission('manage') || !ready.value) return
    const existing = Object.values(workbench.value.tabs).filter(t => t.target.kind === 'display')
    if (existing.length > 0) {
      let next = treeActivateTab(workbench.value, existing[0]!.id)
      for (const extra of existing.slice(1)) {
        next = removeTab(next, extra.id)
      }
      commitWorkbench(next)
      return
    }
    openRegularTab(
      {
        id: DISPLAY_PANEL_ID,
        pinned: false,
        target: { kind: 'display' },
      },
      { groupId, activate: true },
    )
  }

  function openSchedule(scheduleId?: string, title?: string, groupId?: string) {
    if (!hasCurrentPermission('manage') || !ready.value) return
    const bid = (currentBotId.value ?? '').trim()
    const record = ensureBotRecord(bid)
    if (!record) return
    const customTitle = title?.trim() || undefined
    const panelId = scheduleId
      ? `schedule:${scheduleId}`
      : `schedule:new:${record.scheduleCounter + 1}`

    if (workbench.value.tabs[panelId]) {
      let next = updateTabTarget(workbench.value, panelId, t => t.kind === 'schedule'
        ? { ...t, customTitle: customTitle ?? t.customTitle }
        : t)
      next = treeActivateTab(next, panelId)
      commitWorkbench(next)
      return
    }

    if (!scheduleId) {
      patchBotRecord(bid, { scheduleCounter: record.scheduleCounter + 1 })
    }
    openRegularTab(
      {
        id: panelId,
        pinned: false,
        target: { kind: 'schedule', scheduleId, customTitle },
      },
      { groupId, activate: true },
    )
  }

  function uniqueSplitPanelId(baseId: string): string {
    if (!workbench.value.tabs[baseId]) return baseId
    let n = 2
    while (workbench.value.tabs[`${baseId}~${n}`]) n += 1
    return `${baseId}~${n}`
  }

  function splitGroup(groupId: string, direction: 'right' | 'below') {
    if (!ready.value) return
    const group = findGroup(workbench.value.root, groupId)
    const sourceId = group?.activeTabId
    const source = sourceId ? workbench.value.tabs[sourceId] : null
    if (!group || !source) return

    const splitDir: WorkbenchSplitDirection = direction === 'right' ? 'right' : 'bottom'

    const placeCopy = (copy: WorkbenchTab, preview = false) => {
      if (isMobile.value) {
        const { state } = insertTab(workbench.value, copy, {
          groupId: group.id,
          preview,
          activate: true,
          canReplacePreview: id => !fileDirty.value[id],
        })
        commitWorkbench(state)
      } else {
        commitWorkbench(splitGroupWithTab(workbench.value, group.id, splitDir, copy, { preview, activate: true }))
      }
    }

    switch (source.target.kind) {
      case 'chat': {
        const bid = (currentBotId.value ?? '').trim()
        if (!bid) return
        const id = nextChatPanelId(bid)
        const explicitSelection = source.target.explicitSelection
        setNextChatActivationExplicit(id, explicitSelection)
        placeCopy(
          {
            id,
            pinned: false,
            target: {
              kind: 'chat',
              sessionId: source.target.sessionId,
              explicitSelection,
              fallbackTitle: source.target.fallbackTitle,
            },
          },
          true,
        )
        return
      }
      case 'terminal': {
        if (!hasCurrentPermission('workspace_exec')) return
        const bid = (currentBotId.value ?? '').trim()
        const record = ensureBotRecord(bid)
        if (!record) return
        const next = nextTabNumber('terminal', record.terminalCounter)
        patchBotRecord(bid, { terminalCounter: next })
        placeCopy({
          id: `terminal:${next}`,
          pinned: false,
          target: { kind: 'terminal', instance: next },
        })
        return
      }
      case 'browser': {
        if (!hasCurrentPermission('manage')) return
        const bid = (currentBotId.value ?? '').trim()
        const record = ensureBotRecord(bid)
        if (!record) return
        const next = nextTabNumber('browser', record.browserCounter)
        patchBotRecord(bid, { browserCounter: next })
        placeCopy({
          id: `browser:${next}`,
          pinned: false,
          target: {
            kind: 'browser',
            instance: next,
            address: source.target.address,
            customTitle: source.target.customTitle,
          },
        })
        return
      }
      case 'display': {
        if (!hasCurrentPermission('manage')) return
        openDisplay(group.id)
        return
      }
      case 'file':
      case 'preview': {
        if (!hasCurrentPermission('workspace_read')) return
        placeCopy({
          id: uniqueSplitPanelId(source.id),
          pinned: false,
          target: { ...source.target },
        })
        return
      }
      case 'schedule': {
        if (!hasCurrentPermission('manage')) return
        placeCopy({
          id: uniqueSplitPanelId(source.id),
          pinned: false,
          target: { ...source.target },
        })
        return
      }
      case 'asset': {
        placeCopy({
          id: uniqueSplitPanelId(source.id),
          pinned: false,
          target: { ...source.target },
        })
      }
    }
  }

  function updateTerminalTitle(panelId: string, title: unknown) {
    if (!ready.value || typeof title !== 'string') return
    const normalized = title.trim()
    if (!normalized) return
    commitWorkbench(updateTabTarget(workbench.value, panelId, (t) => {
      if (t.kind !== 'terminal' || t.shellTitle === normalized) return t
      return { ...t, shellTitle: normalized }
    }))
  }

  function updateBrowserAddress(panelId: string, address: string) {
    if (!ready.value) return
    commitWorkbench(updateTabTarget(workbench.value, panelId, (t) => {
      if (t.kind !== 'browser') return t
      const customTitle = address ? true : t.customTitle
      if (t.address === address && t.customTitle === customTitle) return t
      return { ...t, address, customTitle }
    }))
  }

  function updateScheduleTitle(panelId: string, title: string) {
    if (!ready.value) return
    const normalized = title.trim()
    commitWorkbench(updateTabTarget(workbench.value, panelId, (t) => {
      if (t.kind !== 'schedule') return t
      const nextCustom = normalized || undefined
      if (t.customTitle === nextCustom) return t
      return { ...t, customTitle: nextCustom }
    }))
  }

  // ---- Close & Unsaved-file confirmation queue -----------------------------

  function shouldKeepLastDraftChat(id: string): boolean {
    const ids = Object.keys(workbench.value.tabs)
    if (ids.length !== 1) return false
    const tab = workbench.value.tabs[id]
    return Boolean(tab && tab.target.kind === 'chat' && tabSessionId(tab) === null)
  }

  function closeTab(id: string) {
    if (!ready.value || shouldKeepLastDraftChat(id)) return
    commitWorkbench(removeTab(workbench.value, id))
  }

  function setFileDirty(panelId: string, dirty: boolean) {
    if (Boolean(fileDirty.value[panelId]) === dirty) return
    const next = { ...fileDirty.value }
    if (dirty) next[panelId] = true
    else delete next[panelId]
    fileDirty.value = next
    if (dirty && ready.value) {
      keepOpenTab(panelId)
    }
  }

  function registerFileSaveHandler(panelId: string, handler: () => Promise<boolean>) {
    saveHandlers.set(panelId, handler)
  }

  function unregisterFileSaveHandler(panelId: string) {
    saveHandlers.delete(panelId)
  }

  const dirtyFileCount = computed(
    () => Object.values(fileDirty.value).filter(Boolean).length,
  )

  function tabDialogLabel(id: string): string {
    const tab = workbench.value.tabs[id]
    if (!tab) return id
    if (tab.target.kind === 'file' || tab.target.kind === 'preview') {
      return workbenchFileBaseName(tab.target.filePath)
    }
    return resolveTabTitle(id)
  }

  const pendingClose = computed<{ panelId: string, title: string } | null>(() => {
    const id = closeQueue.value[0]
    return id ? { panelId: id, title: tabDialogLabel(id) } : null
  })

  function requestCloseTab(id: string) {
    if (!ready.value || !workbench.value.tabs[id]) return
    if (!fileDirty.value[id]) {
      closeTab(id)
      return
    }
    if (!closeQueue.value.includes(id)) {
      closeQueue.value = [...closeQueue.value, id]
    }
  }

  function requestCloseTabs(ids: string[]) {
    if (!ready.value) return
    const queued = [...closeQueue.value]
    let next = workbench.value
    for (const id of ids) {
      if (!next.tabs[id]) continue
      if (fileDirty.value[id]) {
        if (!queued.includes(id)) queued.push(id)
      } else {
        const remainingIds = Object.keys(next.tabs)
        const soleDraft = remainingIds.length === 1
          && next.tabs[id]?.target.kind === 'chat'
          && tabSessionId(next.tabs[id]) === null
        if (!soleDraft) {
          next = removeTab(next, id)
        }
      }
    }
    commitWorkbench(next)
    closeQueue.value = queued
  }

  function closeOtherTabs(groupId: string, keepTabId: string) {
    const group = findGroup(workbench.value.root, groupId)
    if (!group) return
    const targets = group.tabIds.filter(id => id !== keepTabId && !workbench.value.tabs[id]?.pinned)
    requestCloseTabs(targets)
  }

  function closeTabsToRight(groupId: string, fromTabId: string) {
    const group = findGroup(workbench.value.root, groupId)
    if (!group) return
    const idx = group.tabIds.indexOf(fromTabId)
    if (idx < 0) return
    const targets = group.tabIds.slice(idx + 1).filter(id => !workbench.value.tabs[id]?.pinned)
    requestCloseTabs(targets)
  }

  function closeAllTabsInGroup(groupId: string) {
    const group = findGroup(workbench.value.root, groupId)
    if (!group) return
    const targets = group.tabIds.filter(id => !workbench.value.tabs[id]?.pinned)
    requestCloseTabs(targets)
  }

  async function resolvePendingClose(action: 'save' | 'discard' | 'cancel') {
    const id = closeQueue.value[0]
    if (!id) return
    if (action === 'cancel') {
      closeQueue.value = []
      return
    }
    if (action === 'save') {
      const handler = saveHandlers.get(id)
      const ok = handler ? await handler() : true
      if (ok) closeTab(id)
    } else {
      closeTab(id)
    }
    closeQueue.value = closeQueue.value.filter(q => q !== id)
  }

  // ---- Sidebar & shell preferences -----------------------------------------

  const sidebarView = useLocalStorage<SidebarView>('workspace-sidebar-view', 'sessions')
  const sidebarOpen = useLocalStorage('workspace-sidebar-open', true)
  const sidebarWidth = useLocalStorage('workspace-sidebar-width', 256)
  const workbenchOpen = useLocalStorage('workspace-workbench-open', true)
  const mobileNavOpen = ref(false)

  function openMobileNav() { mobileNavOpen.value = true }
  function closeMobileNav() { mobileNavOpen.value = false }
  function setWorkbench(open: boolean) { workbenchOpen.value = open }

  function selectSidebarView(view: SidebarView) {
    if (view === 'files' && !hasCurrentPermission('workspace_read')) return
    sidebarView.value = view
    sidebarOpen.value = true
    setWorkbench(true)
  }

  function toggleWorkbench() { setWorkbench(!workbenchOpen.value) }
  function showWorkbench() { setWorkbench(true) }
  function hideWorkbench() { setWorkbench(false) }

  const pendingFilesPath = ref<string | null>(null)
  function openFilesAt(path: string) {
    if (!hasCurrentPermission('workspace_read')) return
    setWorkbench(true)
    sidebarView.value = 'files'
    sidebarOpen.value = true
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
    if (loadedBotId === bid && ready.value) {
      suppressPersist = true
      try {
        workbench.value = createEmptyWorkbenchState()
        fileDirty.value = {}
        closeQueue.value = []
      } finally {
        suppressPersist = false
      }
    }
    void nextTick(() => clearTerminalSnapshotsForBot(bid))
  }

  function resetAll() {
    storage.value = {}
    pendingFilesPath.value = null
    if (ready.value) {
      suppressPersist = true
      try {
        workbench.value = createEmptyWorkbenchState()
        fileDirty.value = {}
        closeQueue.value = []
      } finally {
        suppressPersist = false
      }
    }
    loadedBotId = null
    void nextTick(() => clearTerminalSnapshots())
  }

  onAuthSessionCleared(() => resetAll())

  watch(currentBotId, (bid) => {
    const next = (bid ?? '').trim()
    if (!next) {
      loadedBotId = null
      return
    }
    ensureBotRecord(next)
    if (ready.value && loadedBotId !== next) {
      restoreForBot(next)
      reconcileDeletedChatPanels()
    }
  }, { immediate: true })

  watch(isMobile, (mobile) => {
    if (!mobile) {
      mobileNavOpen.value = false
    }
  })

  watch(
    () => [currentBotId.value, ...(currentBot.value?.current_user_permissions ?? [])].join('|'),
    () => pruneDisallowedTabs(),
  )

  watch(() => chatStore.draftPromoted, (sig) => {
    if (!sig || !ready.value) return
    const bid = (currentBotId.value ?? '').trim()
    if (!bid || sig.botId !== bid) return
    const tab = workbench.value.tabs[sig.viewId]
    if (!tab || tab.target.kind !== 'chat') return
    const currentSid = tabSessionId(tab)
    if (currentSid && currentSid !== sig.sessionId) return
    if (currentSid !== sig.sessionId) {
      commitWorkbench(updateTabTarget(workbench.value, tab.id, t => t.kind === 'chat'
        ? { ...t, sessionId: sig.sessionId }
        : t))
    }
  }, { flush: 'sync' })

  watch(() => chatStore.userSentInSession, (sig) => {
    if (!sig || !ready.value) return
    const bid = (currentBotId.value ?? '').trim()
    if (!bid || sig.botId !== bid) return
    const tab = workbench.value.tabs[sig.viewId]
    if (!tab || tab.target.kind !== 'chat') return
    const currentSid = tabSessionId(tab)
    let next = workbench.value
    if (sig.wasDraft) {
      if (currentSid && currentSid !== sig.id) return
      next = updateTabTarget(next, tab.id, t => t.kind === 'chat'
        ? { ...t, sessionId: sig.id }
        : t)
    } else if (currentSid !== sig.id) {
      return
    }
    next = treeKeepOpenTab(next, tab.id)
    commitWorkbench(next)
  })

  watch(() => chatStore.draftViewRequested, (request) => {
    if (!request || !ready.value) return
    const bid = (currentBotId.value ?? '').trim()
    if (request.botId !== bid) return
    const tab = workbench.value.tabs[request.viewId]
    const group = findGroupOfTab(workbench.value.root, request.viewId)
    if (!tab || !group || tab.target.kind !== 'chat') return
    if (tabSessionId(tab) !== request.expectedSessionId) return

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

    const id = nextChatPanelId(bid)
    const draftRequest = { ...request, viewId: id }
    if (activate) setNextChatActivationExplicit(id, request.explicitSelection)
    if (!openEphemeralTab(
      {
        id,
        pinned: false,
        target: { kind: 'chat', sessionId: null, explicitSelection: request.explicitSelection },
      },
      { groupId: group.id, activate },
    )) return
    chatStore.applyDraftViewRequest(draftRequest, activate)
  }, { flush: 'sync' })

  watch(() => chatStore.forkedSessionRequested, (request) => {
    if (!request || !ready.value) return
    const bid = (currentBotId.value ?? '').trim()
    if (request.botId !== bid) return
    const tab = workbench.value.tabs[request.viewId]
    if (!tab || tab.target.kind !== 'chat') return
    if (tabSessionId(tab) !== request.expectedSessionId) return
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
    if (loading) return
    alignSelectionWithDock()
  })

  return {
    ready,
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
    ephemeralPanels,
    isPreviewTab,
    keepOpenTab,
    pinPanel,
    pinTab,
    unpinTab,
    togglePinTab,
    dirtyFileCount,
    pendingClose,
    sidebarView,
    sidebarOpen,
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
    resolveTabTitle,
    selectSidebarView,
    toggleWorkbench,
    showWorkbench,
    hideWorkbench,
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
    resetAll,
  }
})
