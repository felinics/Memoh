import { defineStore, storeToRefs } from 'pinia'
import { computed, nextTick, ref, shallowRef, watch } from 'vue'
import { useLocalStorage } from '@vueuse/core'
import { useTabScopedStorage } from '@/utils/tab-scoped-storage'
import { useIsMobile } from '@/composables/useIsMobile'
import type { DockviewApi, DockviewGroupPanel, SerializedDockview } from 'dockview-vue'
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
import i18n from '@/i18n'

// Workspace shell state (activity bar + side panel + dockview layout):
// - the dockview layout in the center area (chat / file / terminal / browser /
//   display panels, splits, tab order) persisted per bot
// - the left activity-bar + side-panel state (active view, collapsed, width)
//
// The active chat session itself lives in the chat-selection store. Chat
// panels are multi-instance dockview views keyed by panel id; the global
// selection still follows whichever chat view is active. Desktop is a
// singleton WebRTC viewer per bot (DISPLAY_PANEL_ID).

export type SidebarView = 'sessions' | 'files' | 'schedule'

export const CHAT_PANEL_ID = 'chat'

/** One desktop WebRTC viewer per bot; reconnect reuses this panel instead of display:2, display:3, … */
export const DISPLAY_PANEL_ID = 'display:1'

export const TERMINAL_TAB_COMPONENT = 'terminalTab'

const DEFAULT_BROWSER_ADDRESS = 'localhost:5173/'
const DEFAULT_CHAT_TITLE = 'New Session'
const DEFAULT_TERMINAL_TITLE = 'Terminal'
// Persisted layouts from older releases used this value as the terminal title.
// It is only recognized for migration; it is never used as a new title.
const LEGACY_TERMINAL_TITLE = 'zsh'

// Default share of the editor height the bottom terminal panel claims when it
// first splits off below the chat. ~1/3 mirrors VS Code's editor:panel ratio
// (≈554:269) — enough room to work in without burying the conversation.
const TERMINAL_PANEL_HEIGHT_RATIO = 1 / 3

export type WorkspacePanelComponent = 'chat' | 'file' | 'preview' | 'asset' | 'terminal' | 'browser' | 'display' | 'schedule'

interface BotLayoutState {
  layout: SerializedDockview | null
  terminalCounter: number
  browserCounter: number
  displayCounter: number
  scheduleCounter: number
  chatCounter: number
  // Panel ids that were ephemeral (preview/draft) when the layout was saved, so
  // the replace behavior survives a reload. Intersected with the panels
  // actually present on restore.
  ephemeralIds: string[]
}

type WorkspaceLayoutStorage = Record<string, BotLayoutState>

function emptyBotLayout(): BotLayoutState {
  return {
    layout: null,
    terminalCounter: 0,
    browserCounter: 0,
    displayCounter: 0,
    scheduleCounter: 0,
    chatCounter: 0,
    ephemeralIds: [],
  }
}

function fileBaseName(filePath: string): string {
  const idx = filePath.lastIndexOf('/')
  return idx >= 0 ? filePath.slice(idx + 1) : filePath
}

function panelComponentOf(id: string): WorkspacePanelComponent | null {
  // Per-session chat tabs use `chat:<n>`. `chat` (legacy singleton) and `chat~2`
  // (legacy split twin) are still recognized so old persisted layouts migrate.
  if (id === CHAT_PANEL_ID) return 'chat'
  if (id.startsWith(`${CHAT_PANEL_ID}~`)) return 'chat'
  if (id.startsWith(`${CHAT_PANEL_ID}:`)) return 'chat'
  if (id.startsWith('file:')) return 'file'
  if (id.startsWith('preview:')) return 'preview'
  if (id.startsWith('asset:')) return 'asset'
  if (id.startsWith('terminal:')) return 'terminal'
  if (id.startsWith('browser:')) return 'browser'
  if (id.startsWith('display:')) return 'display'
  if (id.startsWith('schedule:')) return 'schedule'
  return null
}

function isTerminalOnlyGroup(group: { panels: Array<{ id: string }> }): boolean {
  const panels = group.panels
  return panels.length > 0 && panels.every(p => p.id.startsWith('terminal:'))
}

function syncTerminalGroupChrome(group: DockviewGroupPanel) {
  const terminalOnly = isTerminalOnlyGroup(group)
  group.element.classList.toggle('memoh-terminal-group', terminalOnly)
  const target = terminalOnly ? 'bottom' : 'top'
  if (group.api.getHeaderPosition() !== target) {
    group.api.setHeaderPosition(target)
  }
}

function syncAllTerminalGroupChrome(dock: DockviewApi) {
  for (const group of dock.groups) {
    syncTerminalGroupChrome(group)
  }
}

export const useWorkspaceTabsStore = defineStore('workspace-tabs', () => {
  const selection = useChatSelectionStore()
  const { currentBotId } = storeToRefs(selection)
  const chatStore = useChatStore()
  const currentBot = computed(() =>
    chatStore.bots.find(bot => bot.id === currentBotId.value) ?? null,
  )

  // Mobile shell (see useIsMobile): below 768px the dock runs as a single
  // stack — one group, no tab strip, no splits. Every constraint in this store
  // is keyed off this ONE flag; the desktop path (!isMobile) is untouched.
  const isMobile = useIsMobile()

  // Earlier iterations persisted browser-style tab state under these keys;
  // neither model is compatible with the dockview layout, so drop them.
  if (typeof localStorage !== 'undefined') {
    localStorage.removeItem('workspace-tabs')
    localStorage.removeItem('workspace-panes')
  }
  // Per-Tab: the dockview layout is the full structure of THIS browser tab's
  // panels — two tabs are two windows and must lay out independently. Was
  // localStorage, which shared one layout across all tabs and broadcast every
  // change, so tab A's split reflowed tab B. sessionStorage isolates it; the
  // localStorage cold-start seed (same key) restores "reopen where I left off"
  // after every tab closes, and migrates an existing localStorage layout for free.
  const storage = useTabScopedStorage<WorkspaceLayoutStorage>('workspace-layout', {}, { seed: true })

  // ---- dockview wiring -----------------------------------------------------

  const api = shallowRef<DockviewApi | null>(null)
  const activePanelId = ref<string | null>(null)
  // True while ANY panel/group is being dragged. On the desktop shell the blank
  // header strip (the growing void container) doubles as a window-drag region
  // (`-webkit-app-region: drag`), which swallows native drag-and-drop events so a
  // tab can't land on the blank part of another group's tab bar. chat-workspace
  // mirrors this onto the dock root as `.memoh-dock-dragging`, and the theme
  // flips the void to `no-drag` while it's set so the whole header accepts drops,
  // then back to a window handle on drag end. Also gates the terminal "no-drop"
  // cursor below.
  const panelDragging = ref(false)
  // Per-panel unsaved-changes state for file panels. Kept here (NOT baked into
  // the tab title) so the tab dot, the sidebar count badge, and the close-confirm
  // dialog all read ONE reactive source. Keyed by dockview panel id.
  const fileDirty = ref<Record<string, boolean>>({})
  // VS Code-style "preview tab" state. An ephemeral panel occupies its group's
  // single preview slot, and is replaced in place when another ephemeral-eligible
  // tab opens into that group. It pins (drops out of this map) the first time the
  // user changes it — a file edit, or a message sent in a chat session. The state
  // is NOT surfaced visually (no italic, no marker): it only drives the in-place
  // replacement. Keyed by dockview panel id; persistence reads this ONE reactive
  // source. Mirrors the fileDirty pattern above.
  const ephemeralPanels = ref<Record<string, true>>({})
  // Save callbacks registered by each mounted file panel, so a dirty tab can be
  // written from the close-confirm dialog even while it sits in the background.
  const saveHandlers = new Map<string, () => Promise<boolean>>()
  // FIFO of dirty panel ids awaiting a close decision (drives the dialog). A
  // batch close (others / all) enqueues several; the dialog walks them in turn.
  const closeQueue = ref<string[]>([])
  // The bot whose layout is currently loaded into dockview. Guards persistence
  // so that clearing the view during a bot switch can't wipe the old bot's
  // stored layout.
  let loadedBotId: string | null = null
  let suppressPersist = false
  // True while the in-memory dock holds a mobile-shaped stack that no DESKTOP
  // restoreLayout has reconciled with the persisted desktop layout yet — set
  // on entering mobile and by every mobile restoreLayout, cleared by the next
  // desktop one. Keeps persistLayout sealed in the post-exit window (see
  // persistLayout and the isMobile watcher for why the exit path alone is
  // not enough).
  let dockHoldsMobileStack = false
  let apiDisposables: Array<{ dispose(): void }> = []
  // Whether the in-flight drag started from a terminal. Non-terminal drags lock
  // the exclusive terminal groups (see beginDrag) so an editor can never merge
  // into the bottom panel; terminal drags leave them open so terminals can be
  // reordered or stacked. Reset on drag end.
  let dragSourceTerminal = false
  let draftChatQueued = false
  // After restoring a NON-empty dock, ignore initialize()'s auto-picked
  // sessionId (and other non-explicit selection churn). Otherwise a File /
  // Preview workspace gets an extra Untitled/New Session tab punched in by
  // selection watchers — the cold-start hijack this store exists to prevent.
  // Explicit user actions (sidebar click, New Session, /new) either call the
  // public open* APIs directly or set hasExplicitSessionSelection, both of
  // which bypass this guard. Cleared when the dock is empty again.
  let suppressSelectionDockMutations = false
  const reconcilingDeletedChatPanelIds = new Set<string>()
  const deletedSessionIdsByBot = new Map<string, Set<string>>()
  let suppressReconcileActivation = false
  let reconcileActivationReleaseToken = 0
  let reconcileActivationReleaseTimer: ReturnType<typeof setTimeout> | null = null
  const chatActivationExplicitOverrides = new Map<string, boolean>()
  // The most recent NON-terminal group to hold focus. A terminal group is
  // exclusive — opening a file/browser/etc. while it is the active group must
  // land in an editor group, not contaminate the terminals — so we remember the
  // last editor group to route those opens back to (see nonTerminalTarget).
  let lastNonTerminalGroupId: string | null = null

  function ensureBotLayout(botId: string | null | undefined): BotLayoutState | null {
    const bid = (botId ?? '').trim()
    if (!bid) return null
    if (!storage.value[bid]) {
      storage.value = { ...storage.value, [bid]: emptyBotLayout() }
    }
    const state = storage.value[bid]
    if (state && (typeof state.scheduleCounter !== 'number'
      || typeof state.chatCounter !== 'number'
      || !Array.isArray(state.ephemeralIds))) {
      storage.value = { ...storage.value, [bid]: { ...emptyBotLayout(), ...state } }
    }
    return storage.value[bid] ?? null
  }

  function patchBotLayout(botId: string, patch: Partial<BotLayoutState>) {
    const state = ensureBotLayout(botId)
    if (!state) return
    storage.value = { ...storage.value, [botId]: { ...state, ...patch } }
  }

  // Dockview serializes `hideHeader: true` when a group's tab bar is hidden
  // (display:none). Once this enters localStorage it re-applies on every
  // fromJSON call, creating a permanent cycle where the tab bar never appears.
  // Strip the flag from all leaf group data so the tab bar is always visible,
  // both when reading from storage and when writing back to storage.
  function sanitizeLayout(layout: SerializedDockview): SerializedDockview {
    type GridNode = { type: string; data: unknown }
    function walkNode(node: GridNode): GridNode {
      if (!node || typeof node !== 'object') return node
      if (node.type === 'leaf' && node.data && typeof node.data === 'object') {
        const data = { ...(node.data as Record<string, unknown>) }
        delete data.hideHeader
        // Defensively strip `locked`: older builds toggled 'no-drop-target' on
        // terminal groups mid-drag and a layout-change persist could bake it
        // into storage, permanently sealing the group on the next restore. Drop
        // rejection is now an onWillDrop veto (see registerApi), so we never set
        // it — this just scrubs any value an old snapshot still carries.
        delete data.locked
        return { ...node, data }
      }
      if (node.type === 'branch' && Array.isArray(node.data)) {
        return { ...node, data: (node.data as GridNode[]).map(walkNode) }
      }
      return node
    }
    // Strip any persisted per-panel tabComponent (older layouts pinned terminals
    // to 'terminalTab'): terminals now resolve their tab through the default host
    // by group, so a baked component would override that and keep a moved-out
    // terminal stuck as a chip.
    function stripTabComponents(panels: SerializedDockview['panels']): SerializedDockview['panels'] {
      if (!panels || typeof panels !== 'object') return panels
      const out: Record<string, unknown> = {}
      for (const [id, panel] of Object.entries(panels)) {
        if (panel && typeof panel === 'object' && 'tabComponent' in panel) {
          const { tabComponent: _tabComponent, ...rest } = panel as unknown as Record<string, unknown>
          out[id] = rest
        } else {
          out[id] = panel
        }
      }
      return out as SerializedDockview['panels']
    }
    try {
      return {
        ...layout,
        grid: {
          ...layout.grid,
          root: walkNode(layout.grid.root as GridNode) as SerializedDockview['grid']['root'],
        },
        panels: stripTabComponents(layout.panels),
      }
    } catch {
      return layout
    }
  }

  function persistLayout() {
    // Mobile never serializes: its single stack is a runtime constraint, not a
    // user arrangement, and must not overwrite the desktop multi-group layout
    // stored for this bot (the tab-scoped storage is shared across form
    // factors, so a mobile write would clobber the desktop's restore seed).
    // dockHoldsMobileStack extends that seal past the breakpoint exit:
    // crossing back to desktop only un-hides the tab strips, the dock is still
    // the merged stack, and the next onDidLayoutChange (tab activation, an
    // async panel title) would otherwise serialize it over the desktop
    // snapshot before the next desktop restoreLayout rebuilds from it.
    if (!api.value || suppressPersist || !loadedBotId || isMobile.value || dockHoldsMobileStack) return
    try {
      const dock = api.value
      const ephemeralIds = Object.keys(ephemeralPanels.value).filter(id => dock.getPanel(id))
      patchBotLayout(loadedBotId, { layout: sanitizeLayout(dock.toJSON()), ephemeralIds })
    } catch {
      // Serialization should never throw, but a failed save must not break UI.
    }
  }

  function focusPanel(panel: unknown) {
    (panel as { api?: { setActive?: () => void } }).api?.setActive?.()
  }

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

  function numberedFallbackTitle(prefix: string, id: string): string {
    const suffix = id.split(':')[1]?.trim()
    return suffix ? `${prefix} ${suffix}` : prefix
  }

  function terminalTitleFallback(id: string): string {
    const prefix = i18n.global.t('bots.terminal.defaultTabLabel').trim() || DEFAULT_TERMINAL_TITLE
    return numberedFallbackTitle(prefix, id)
  }

  // Per-session chat title fallback (English; the sidebar callers pass localized
  // strings, and syncChatTitles overlays the server title once known).
  function chatTitleFallbackFor(sid: string | null): string {
    if (!sid) return DEFAULT_CHAT_TITLE
    const session = chatStore.knownSessionSummary(sid)
    return (session?.title ?? '').trim() || i18n.global.t('chat.untitledSession')
  }

  function panelTitleFallback(panel: { id: string, params?: Record<string, unknown> }): string {
    const params = panel.params ?? {}
    switch (panelComponentOf(panel.id)) {
      case 'chat':
        return chatTitleFallbackFor(panelSessionId(panel))
      case 'file':
        return fileBaseName(panel.id.slice('file:'.length))
      case 'preview':
        return fileBaseName(panel.id.slice('preview:'.length))
      case 'asset': {
        const name = params.name
        return typeof name === 'string' && name.trim() ? name.trim() : 'file'
      }
      case 'terminal':
        return terminalTitleFallback(panel.id)
      case 'browser': {
        const address = params.address
        return typeof address === 'string' && address.trim()
          ? address.trim()
          : numberedFallbackTitle('Browser', panel.id)
      }
      case 'display':
        return numberedFallbackTitle('Desktop', panel.id)
      case 'schedule':
        return 'Schedule'
      default:
        return panel.id
    }
  }

  function repairEmptyPanelTitles(): boolean {
    const dock = api.value
    if (!dock) return false
    let repaired = false
    for (const panel of dock.panels) {
      const title = (panel.api.title ?? '').trim()
      const isLegacyTerminalTitle = panelComponentOf(panel.id) === 'terminal'
        && title.toLowerCase() === LEGACY_TERMINAL_TITLE
      if (title && !isLegacyTerminalTitle) continue
      panel.api.setTitle(panelTitleFallback(panel))
      repaired = true
    }
    return repaired
  }

  function updateTerminalTitle(panelId: string, title: unknown) {
    const dock = api.value
    if (!dock || panelComponentOf(panelId) !== 'terminal' || typeof title !== 'string') return
    const panel = dock.getPanel(panelId)
    const normalized = title.trim()
    if (!panel || !normalized || panel.api.title === normalized) return
    panel.api.setTitle(normalized)
    persistLayout()
  }

  function restoreLayout(botId: string) {
    const dock = api.value
    if (!dock) return
    let dockEmptyAfterRestore = false
    let repairedEmptyTitles = false
    suppressPersist = true
    try {
      const stored = storage.value[botId]?.layout ?? null
      // Mobile never restores the persisted layout: that tree is the desktop
      // multi-group arrangement, and deserializing it would resurrect splits
      // the mobile shell forbids. The empty branch below drives the mobile
      // cold start instead — draft fallback + chat-selection rebind.
      if (stored && !isMobile.value) {
        try {
          dock.fromJSON(sanitizeLayout(stored))
        } catch {
          dock.clear()
          patchBotLayout(botId, { layout: null })
        }
      } else {
        dock.clear()
      }
      loadedBotId = botId
      // The dock now holds exactly what this restore put in it: the desktop
      // snapshot (desktop restore) or a fresh mobile stack (mobile restore —
      // the snapshot branch above is skipped). Only a DESKTOP restore unseals
      // persistence; a mobile one keeps the seal on for the eventual exit.
      dockHoldsMobileStack = isMobile.value
      prunePanels()
      // Restore the ephemeral slot from storage, keeping only ids that survived
      // restore + prune. Wholesale replace so the previous bot's flags are dropped.
      const storedEphemeral = storage.value[botId]?.ephemeralIds ?? []
      const nextEphemeral: Record<string, true> = {}
      for (const id of storedEphemeral) {
        if (dock.getPanel(id)) nextEphemeral[id] = true
      }
      ephemeralPanels.value = nextEphemeral
      activePanelId.value = dock.activePanel?.id ?? null
      syncAllTerminalGroupChrome(dock)
      repairedEmptyTitles = repairEmptyPanelTitles()
      dockEmptyAfterRestore = dock.panels.length === 0
    } finally {
      suppressPersist = false
    }
    if (repairedEmptyTitles && !dockEmptyAfterRestore) persistLayout()
    // Non-empty restore is authoritative for this tab's panels. Empty restore
    // still needs the draft fallback, and may accept initialize()'s auto-pick
    // (repointing that draft). Same product rule either way: never invent tabs
    // on top of a restored workspace.
    suppressSelectionDockMutations = !dockEmptyAfterRestore
    if (dockEmptyAfterRestore) ensureDraftChatPanel()
    syncDraftChatExplicitSelection()
    syncRestoredChatSelection({ preserveRestoredChat: true })
  }

  function domListener<K extends keyof DocumentEventMap>(
    type: K,
    handler: (event: DocumentEventMap[K]) => void,
    options?: AddEventListenerOptions,
  ): { dispose(): void } {
    if (typeof document === 'undefined') return { dispose() {} }
    document.addEventListener(type, handler as EventListener, options)
    return {
      dispose() {
        document.removeEventListener(type, handler as EventListener, options)
      },
    }
  }

  // A terminal group is exclusive: an editor (file/preview/chat/…) can never
  // become a tab inside it, mirroring how a VS Code terminal panel refuses
  // editor drops. We enforce this per drag with onWillShowOverlay (veto the
  // drop overlay) and onWillDrop (veto the drop) — see registerApi. Preventing
  // the overlay routes through dockview's removeDropTarget, so no stale
  // "can-drop" highlight lingers over the terminal. The group-level `locked`
  // flag can't do that: it makes canDisplayOverlay bail out early and the
  // shared overlay element is left untouched, so the highlight from the group
  // hovered just before stays painted. beginDrag only records WHAT is being
  // dragged so those vetoes can tell a foreign editor from a terminal session
  // being reordered.
  function beginDrag(sourceTerminal: boolean) {
    panelDragging.value = true
    dragSourceTerminal = sourceTerminal
  }

  function endDrag() {
    panelDragging.value = false
    dragSourceTerminal = false
  }

  // Is the in-flight drag a NON-terminal panel/group? A single-tab drag carries
  // its panelId; a whole-group drag has a null panelId, so we fall back to the
  // terminal-vs-editor classification recorded at drag start.
  function draggingNonTerminal(
    event: { getData(): { panelId?: string | null } | undefined },
  ): boolean {
    const panelId = event.getData()?.panelId
    if (typeof panelId === 'string') return !panelId.startsWith('terminal:')
    return !dragSourceTerminal
  }

  // ---- mobile single-stack constraint --------------------------------------
  // Below 768px the dock is ONE group with its tab strip hidden: a stack of
  // full-screen panels where "open X" pushes onto the stack and the top bar's
  // back affordance re-activates the chat panel (secondary panels are never
  // closed, so terminals/WebRTC keep running in the background). All of this
  // is runtime-only — the persisted desktop layout is neither read
  // (restoreLayout) nor written (persistLayout) while mobile.

  // Merge every group into the first one. group.api.moveTo(center) moves all
  // of a source group's panels into the target and dissolves the emptied
  // source; dockview suppresses its per-panel remove/add events mid-move, so
  // panel state (dirty flags, ephemeral slots) survives the merge.
  function mergeGroupsIntoSingle(dock: DockviewApi) {
    const [first, ...rest] = dock.groups
    if (!first) return
    for (const group of rest) {
      group.api.moveTo({ group: first, position: 'center' })
    }
  }

  // Hide the tab strip while mobile, restore it when leaving. The
  // group.model.header.hidden setter is a pure display toggle (no events, no
  // measurement), so running this on every layout change is cheap and
  // self-healing: groups created AFTER entering mobile (cold-start draft, bot
  // switch) get hidden as they appear, and rebuilt groups (fromJSON) re-hide
  // even though the flag itself is never persisted.
  function syncMobileGroupHeaders(dock: DockviewApi) {
    for (const group of dock.groups) {
      if (group.model.header.hidden !== isMobile.value) {
        group.model.header.hidden = isMobile.value
      }
    }
  }

  function registerApi(dock: DockviewApi) {
    for (const d of apiDisposables) d.dispose()
    api.value = dock
    apiDisposables = [
      dock.onDidActivePanelChange((event) => {
        // dockview v7 renamed this event's payload field: v6 fired the panel on
        // `event.activePanel`, v7 fires `{ panel, origin }`. Reading the old name
        // yields undefined every time, silently nulling activePanelId on every
        // tab switch — which desyncs the sidebar file-tree highlight (its only
        // consumer via activeId). Use `event.panel`.
        const panel = event.panel
        activePanelId.value = panel?.id ?? null
        const group = panel?.group
        if (group && !isTerminalOnlyGroup(group)) {
          lastNonTerminalGroupId = group.id
        }
        // Activating a chat tab makes its session the live one. Strictly gated so
        // file/terminal activation never touches chat state. During deleted-tab
        // reconciliation dockview may auto-activate a neighboring tab; ignore that
        // transient activation so it cannot override chat-list's chosen fallback
        // session.
        if (!suppressReconcileActivation && panel && panelComponentOf(panel.id) === 'chat') {
          activateChatSession(panel)
        }
      }),
      dock.onDidLayoutChange(() => {
        // Catch-all: keep terminal-group chrome (bottom header + class) in
        // lockstep with composition for ANY layout mutation, including drags
        // dockview may not surface through onDidMovePanel. The per-tab host
        // also keys off layout changes, so syncing here guarantees the group
        // chrome and the chip-vs-normal tab never disagree. setHeaderPosition
        // is guarded by a current-value check, so this cannot loop. Sync first
        // so the persisted snapshot already reflects the resolved chrome.
        syncAllTerminalGroupChrome(dock)
        // Also re-apply the mobile tab-strip visibility: it is runtime-only
        // state, so groups added or rebuilt by a restore must pick it up here.
        syncMobileGroupHeaders(dock)
        persistLayout()
      }),
      dock.onDidRemovePanel((panel) => {
        cleanupPanelState(panel.id)
        if (panel.id.startsWith('terminal:') && loadedBotId) {
          const bid = loadedBotId
          void nextTick(() => deleteTerminalSnapshot(terminalCacheKey(bid, panel.id)))
        }
        const closingDeletedChat = reconcilingDeletedChatPanelIds.delete(panel.id)
        if (closingDeletedChat && reconcilingDeletedChatPanelIds.size === 0) {
          releaseDeletedChatActivationAfterRemove()
        }
        // Closing the LAST chat tab resets the global view to a fresh draft, so the
        // dock respawns a "New Session" page instead of reopening the session that
        // was just closed (its id is still the global one until we clear it). A
        // reconcile-driven close is different: chat-list has already selected the
        // next valid session, so do not clear that selection back to draft.
        if (
          !closingDeletedChat
          && panelComponentOf(panel.id) === 'chat'
          && !dock.panels.some(p => panelComponentOf(p.id) === 'chat')
        ) {
          chatStore.selectDraft({ explicitSelection: false })
        }
        syncAllTerminalGroupChrome(dock)
        ensureDraftChatPanel()
      }),
      dock.onDidAddPanel(() => {
        syncAllTerminalGroupChrome(dock)
      }),
      dock.onDidMovePanel(() => {
        syncAllTerminalGroupChrome(dock)
      }),
      // Record what is being dragged (a terminal session vs a foreign editor)
      // and flip the desktop window-drag region off so blank header space
      // accepts drops.
      dock.onWillDragPanel((event) => {
        beginDrag(event.panel.id.startsWith('terminal:'))
      }),
      dock.onWillDragGroup((event) => {
        beginDrag(isTerminalOnlyGroup(event.group))
      }),
      // Terminal groups are exclusive. Veto the drop overlay so no "can-drop"
      // highlight ever paints over the terminal, and veto the drop itself,
      // whenever a foreign editor is dragged onto one (header, tab strip or
      // content, any edge). Terminal sessions stay droppable among themselves.
      dock.onWillShowOverlay((event) => {
        // Mobile runs a single group: veto any drop that would split off a new
        // group. A 'center' drop merges as a tab inside the group — the only
        // arrangement the mobile shell allows — so it stays droppable.
        if (isMobile.value && event.position !== 'center') {
          event.preventDefault()
          return
        }
        if (isTerminalOnlyGroup(event.group) && draggingNonTerminal(event)) {
          event.preventDefault()
        }
      }),
      dock.onWillDrop((event) => {
        if (isMobile.value && event.position !== 'center') {
          event.preventDefault()
          return
        }
        if (isTerminalOnlyGroup(event.group) && draggingNonTerminal(event)) {
          event.preventDefault()
        }
      }),
      // Clear the drag flags however the drag ends (drop, cancel, Esc). Native
      // 'dragend' always fires for the HTML5 backend; 'pointerup' covers the
      // pointer/touch backend.
      domListener('dragend', () => endDrag(), { capture: true }),
      domListener('pointerup', () => endDrag(), { capture: true }),
      // The vetoes above already refuse foreign editors, but dockview still
      // preventDefaults the native dragover, so the OS would otherwise paint a
      // droppable cursor. Force the "no-drop" cursor over the terminal group
      // while a non-terminal is in flight. Capture phase so this runs before
      // dockview's own dragover sets the effect.
      domListener('dragover', (event) => {
        if (!panelDragging.value || dragSourceTerminal) return
        const target = event.target
        if (!(target instanceof Element)) return
        if (!target.closest('.memoh-terminal-group')) return
        if (event.dataTransfer) event.dataTransfer.dropEffect = 'none'
      }, { capture: true }),
    ]
    const bid = (currentBotId.value ?? '').trim()
    if (bid) {
      if (deletedSessionIdsByBot.get(bid)?.size) holdDeletedChatActivation()
      restoreLayout(bid)
      reconcileDeletedChatPanels()
    }
  }

  function holdDeletedChatActivation() {
    suppressReconcileActivation = true
    reconcileActivationReleaseToken++
    if (reconcileActivationReleaseTimer) {
      clearTimeout(reconcileActivationReleaseTimer)
      reconcileActivationReleaseTimer = null
    }
  }

  function releaseDeletedChatActivationAfterRemove() {
    const token = ++reconcileActivationReleaseToken
    if (reconcileActivationReleaseTimer) clearTimeout(reconcileActivationReleaseTimer)
    reconcileActivationReleaseTimer = setTimeout(() => {
      reconcileActivationReleaseTimer = null
      if (token === reconcileActivationReleaseToken && reconcilingDeletedChatPanelIds.size === 0) {
        suppressReconcileActivation = false
        const active = api.value?.activePanel
        const activeSid = active ? panelSessionId(active) : null
        const selectedSid = (selection.sessionId ?? '').trim()
        if (
          active
          && panelComponentOf(active.id) === 'chat'
          && activeSid
          && (!selectedSid || activeSid === selectedSid)
          && !isDeletedSessionForCurrentBot(activeSid)
        ) {
          activateChatSession(active, { explicitSelection: chatStore.hasExplicitSessionSelection === true })
        }
      }
    }, 0)
  }

  function releaseDeletedChatActivationNow() {
    reconcileActivationReleaseToken++
    if (reconcileActivationReleaseTimer) {
      clearTimeout(reconcileActivationReleaseTimer)
      reconcileActivationReleaseTimer = null
    }
    if (reconcilingDeletedChatPanelIds.size !== 0) return
    suppressReconcileActivation = false
    const active = api.value?.activePanel
    const activeSid = active ? panelSessionId(active) : null
    const selectedSid = (selection.sessionId ?? '').trim()
    if (
      active
      && panelComponentOf(active.id) === 'chat'
      && activeSid
      && (!selectedSid || activeSid === selectedSid)
      && !isDeletedSessionForCurrentBot(activeSid)
    ) {
      activateChatSession(active, { explicitSelection: chatStore.hasExplicitSessionSelection === true })
    }
  }

  function ensureSelectedChatPanel() {
    const sid = (selection.sessionId ?? '').trim()
    if (!sid || isDeletedSessionForCurrentBot(sid)) return
    if (chatPanelForSession(sid)) return
    const bid = (currentBotId.value ?? '').trim()
    if (!bid) return
    const id = nextChatPanelId(bid)
    setNextChatActivationExplicit(id, chatStore.hasExplicitSessionSelection === true)
    if (focusOrAdd({
      id,
      component: 'chat',
      title: chatTitleFallbackFor(sid),
      params: {
        sessionId: sid,
        explicitSelection: chatStore.hasExplicitSessionSelection === true,
      },
    })) {
      markEphemeral(id)
    }
  }

  function releaseApi() {
    for (const d of apiDisposables) d.dispose()
    apiDisposables = []
    api.value = null
    activePanelId.value = null
    loadedBotId = null
    panelDragging.value = false
    dragSourceTerminal = false
    draftChatQueued = false
    suppressSelectionDockMutations = false
    reconcilingDeletedChatPanelIds.clear()
    releaseDeletedChatActivationNow()
  }

  // ---- panel operations ----------------------------------------------------

  const activeId = computed<string | null>(() => activePanelId.value)

  // The mobile top bar picks its left affordance from this: chat active → "≡"
  // opens the navigation; a secondary panel (terminal/browser/…) active → "←"
  // runs activateChatPanel. An empty dock (mid bot-switch) counts as chat so
  // the fallback affordance is the navigation, not a dead back button.
  const activePanelIsChat = computed(() => {
    const id = activePanelId.value
    return !id || panelComponentOf(id) === 'chat'
  })

  function hasCurrentPermission(permission: BotPermission): boolean {
    return hasBotPermission(currentBot.value?.current_user_permissions, permission)
  }

  // Resolve a non-terminal group to anchor against. A terminal-only group is
  // exclusive, so we never let an editor panel land in / split off one: prefer
  // the caller's group (if not terminal), then the active group, then the last
  // editor group to hold focus, then any non-terminal group. Undefined → there is
  // no editor group to anchor against (empty dock, or only terminal groups exist).
  // This is the single source of the candidate-priority order; nonTerminalTarget
  // wraps it for the "join a group" case, openFileToSide uses the group directly
  // for the "split beside a group" case.
  function nonTerminalAnchorGroup(
    dock: DockviewApi,
    groupId?: string,
  ): DockviewGroupPanel | undefined {
    const explicit = groupId ? dock.getGroup(groupId) : undefined
    if (explicit && !isTerminalOnlyGroup(explicit)) return explicit
    const active = dock.activeGroup
    if (active && !isTerminalOnlyGroup(active)) return active
    if (lastNonTerminalGroupId) {
      const last = dock.getGroup(lastNonTerminalGroupId)
      if (last && !isTerminalOnlyGroup(last)) return last
    }
    return dock.groups.find(g => !isTerminalOnlyGroup(g))
  }

  // Resolve a group an EDITOR-class panel may JOIN. Reuses nonTerminalAnchorGroup
  // for the candidate priority (caller → active → last editor → any non-terminal),
  // then returns it as a 'within' target. When no non-terminal group exists but
  // terminal groups do, open ABOVE the first group so the panel gets its own group
  // instead of joining the terminals — except on mobile, where the single-stack
  // constraint overrides terminal exclusivity and the panel joins the one group
  // (reachable only via a cross-device session delete closing the last editor
  // while a terminal stays open). Undefined → empty dock (let dockview create
  // the first group).
  function nonTerminalTarget(
    dock: DockviewApi,
    groupId?: string,
  ): { referenceGroup: string, direction: 'within' | 'above' } | undefined {
    const anchor = nonTerminalAnchorGroup(dock, groupId)
    if (anchor) return { referenceGroup: anchor.id, direction: 'within' }
    const fallback = dock.groups[0]
    if (fallback) {
      const direction = isMobile.value ? 'within' : 'above'
      return { referenceGroup: fallback.id, direction }
    }
    return undefined
  }

  function focusOrAdd(options: {
    id: string
    component: WorkspacePanelComponent
    title: string
    params?: Record<string, unknown>
    // The tab group whose header strip triggered the open (the "+" lives per
    // group). Without it dockview drops the new panel into the active group,
    // which is why a "+" in the right split used to open its terminal back in
    // the left pane.
    groupId?: string
  }): boolean {
    const dock = api.value
    if (!dock) return false
    const existing = dock.getPanel(options.id)
    if (existing) {
      focusPanel(existing)
      return true
    }
    const target = nonTerminalTarget(dock, options.groupId)
    dock.addPanel({
      id: options.id,
      component: options.component,
      title: options.title,
      params: options.params,
      // Keep panel DOM mounted while hidden: terminals, WebRTC display and
      // chat scroll state do not survive detach/reattach cycles.
      renderer: 'always',
      ...(target ? { position: target } : {}),
    })
    return true
  }

  function markEphemeral(id: string) {
    if (ephemeralPanels.value[id]) return
    ephemeralPanels.value = { ...ephemeralPanels.value, [id]: true }
  }

  // Pin a panel: drop it out of the ephemeral slot so it stops getting replaced.
  // Idempotent. Triggered by the first user change (file edit or chat message);
  // never reversed (VS Code keeps an edited tab pinned).
  function pinPanel(id: string) {
    if (!ephemeralPanels.value[id]) return
    const next = { ...ephemeralPanels.value }
    delete next[id]
    ephemeralPanels.value = next
  }

  // Open (or focus) an ephemeral-slot panel. Each non-terminal group holds at most
  // one ephemeral panel; opening another ephemeral-eligible tab into that group
  // replaces it IN PLACE (add the new one first, then close the old so the group
  // never empties mid-swap). Ephemeral panels are never dirty, so the close needs
  // no save prompt. An existing panel for the same id is just focused (its current
  // pinned/ephemeral state is preserved).
  function openEphemeral(opts: {
    id: string
    component: WorkspacePanelComponent
    title: string
    params?: Record<string, unknown>
    groupId?: string
    inactive?: boolean
  }): boolean {
    const dock = api.value
    if (!dock) return false
    const existing = dock.getPanel(opts.id)
    if (existing) {
      if (!opts.inactive) focusPanel(existing)
      return true
    }
    const target = nonTerminalTarget(dock, opts.groupId)
    // Only an in-place join ('within' an existing editor group) has a previous
    // ephemeral to replace; opening a brand-new group never does.
    const targetGroupId = target?.direction === 'within' ? target.referenceGroup : undefined
    const prevEphemeral = targetGroupId
      ? dock.getGroup(targetGroupId)?.panels.find(
          p => ephemeralPanels.value[p.id] && p.id !== opts.id,
        )
      : undefined
    dock.addPanel({
      id: opts.id,
      component: opts.component,
      title: opts.title,
      params: opts.params,
      renderer: 'always',
      inactive: opts.inactive,
      ...(target ? { position: target } : {}),
    })
    markEphemeral(opts.id)
    prevEphemeral?.api.close()
    return true
  }

  function defaultTerminalPosition(dock: DockviewApi, belowGroupId?: string) {
    // Reuse the bottom terminal panel (a terminal-ONLY group) if one exists, so a
    // new session stacks as another tab instead of spawning a parallel panel. We
    // match terminal-ONLY (not "has a terminal"): a terminal dragged up into the
    // chat group lives in a MIXED group, which is no longer the bottom panel.
    const terminalGroupId = dock.groups.find(g => isTerminalOnlyGroup(g))?.id
    if (terminalGroupId) {
      return { referenceGroup: terminalGroupId, direction: 'within' as const }
    }
    // No bottom panel yet: open one directly BELOW the column the request came
    // from — the editor group whose "+" was clicked, else the active editor
    // group, else the chat group. This is why a terminal opened from the RIGHT
    // split now appears under the right split instead of jumping back under the
    // chat on the left.
    const below
      = (belowGroupId ? dock.getGroup(belowGroupId) : undefined)
        ?? (dock.activeGroup && !isTerminalOnlyGroup(dock.activeGroup) ? dock.activeGroup : undefined)
        ?? dock.groups.find(g => g.panels.some(p => panelComponentOf(p.id) === 'chat'))
    if (below && !isTerminalOnlyGroup(below)) {
      return { referenceGroup: below.id, direction: 'below' as const }
    }
    return undefined
  }

  function addTerminalPanel(options: {
    id: string
    title: string
    groupId?: string
    position?: { referenceGroup: string, direction: 'within' | 'below' | 'right' | 'left' | 'above' }
  }) {
    const dock = api.value
    if (!dock) return false
    const existing = dock.getPanel(options.id)
    if (existing) {
      existing.api.setActive()
      syncAllTerminalGroupChrome(dock)
      return true
    }
    // No per-panel tabComponent: terminals use the default tab host, which
    // renders the file-chip tab ONLY while the panel sits in a terminal-only
    // group and falls back to the normal editor tab once a terminal is dragged
    // into a mixed group — so a moved-out terminal blends into the dock strip.
    const panelBase = {
      id: options.id,
      component: 'terminal' as const,
      title: options.title,
      renderer: 'always' as const,
    }
    // Mobile forbids the bottom split: a terminal joins the single stack as a
    // tab, anchored through the same editor-group resolution as other opens.
    const position = isMobile.value
      ? nonTerminalTarget(dock, options.groupId)
      : options.groupId
        ? { referenceGroup: options.groupId, direction: 'within' as const }
        : options.position
    const panel = dock.addPanel({
      ...panelBase,
      ...(position ? { position } : {}),
    })
    // Only when the terminal opens its OWN new group BELOW the chat: give that
    // group a sensible default height instead of dockview's even 50/50 split.
    // Joining an existing terminal/editor group keeps whatever height it has.
    if (position?.direction === 'below' && dock.height > 0) {
      panel.group.api.setSize({
        height: Math.round(dock.height * TERMINAL_PANEL_HEIGHT_RATIO),
      })
    }
    syncAllTerminalGroupChrome(dock)
    return true
  }

  // The session a chat panel renders, read from its dockview params (null = draft).
  function panelSessionId(panel: { params?: Record<string, unknown> }): string | null {
    const sid = panel.params?.sessionId
    return typeof sid === 'string' && sid.trim() ? sid : null
  }

  function chatPanelForSession(sid: string) {
    const dock = api.value
    if (!dock) return undefined
    return dock.panels.find(
      p => panelComponentOf(p.id) === 'chat' && panelSessionId(p) === sid,
    )
  }

  function resetDeletedChatPanelToDraft(panel: { id: string, api: { updateParameters(params: Record<string, unknown>): void, setTitle(title: string): void } }) {
    const explicitSelection = chatStore.hasExplicitSessionSelection === true
    panel.api.updateParameters({ sessionId: null, explicitSelection })
    panel.api.setTitle(DEFAULT_CHAT_TITLE)
    markEphemeral(panel.id)
    if (api.value?.activePanel?.id === panel.id) {
      chatStore.selectDraft({ explicitSelection })
    }
  }

  function isDeletedSessionForCurrentBot(sid: string | null): boolean {
    const bid = (currentBotId.value ?? '').trim()
    if (!bid || !sid) return false
    const latestDeleted = chatStore.deletedSession
    if (latestDeleted?.botId === bid && latestDeleted.id === sid) return true
    return Boolean(deletedSessionIdsByBot.get(bid)?.has(sid))
  }

  function nextChatPanelId(bid: string): string {
    const state = ensureBotLayout(bid)
    const next = (state?.chatCounter ?? 0) + 1
    patchBotLayout(bid, { chatCounter: next })
    return `chat:${next}`
  }

  /** Open or focus a chat tab for a session. An already-open tab for that session
   * is focused; otherwise the open reuses the target group's ephemeral slot (so
   * browsing sessions in the sidebar swaps one preview tab, VS Code-style). The
   * tab starts ephemeral and pins when the user sends a message in it. */
  function openSessionChat(opts: {
    sessionId: string
    title?: string
    groupId?: string
    explicitSelection?: boolean
    groupLocal?: boolean
    activate?: boolean
  }) {
    const dock = api.value
    if (!dock) return
    const sid = opts.sessionId.trim()
    if (!sid) return
    const bid = (currentBotId.value ?? '').trim()
    if (!bid) return
    if (isDeletedSessionForCurrentBot(sid)) return
    const title = opts.title?.trim() || chatTitleFallbackFor(sid)
    const explicitSelection = opts.explicitSelection !== false
    const activate = opts.activate !== false
    const existing = opts.groupLocal && opts.groupId
      ? dock.getGroup(opts.groupId)?.panels.find(
          panel => panelComponentOf(panel.id) === 'chat' && panelSessionId(panel) === sid,
        )
      : chatPanelForSession(sid)
    if (existing) {
      existing.api.setTitle(title)
      existing.api.updateParameters({ explicitSelection })
      if (!activate) return
      if (dock.activePanel?.id === existing.id) {
        selectChatSession(sid, explicitSelection)
        return
      }
      setNextChatActivationExplicit(existing.id, explicitSelection)
      focusPanel(existing)
      return
    }
    // Repoint the target group's existing ephemeral CHAT slot in place (no
    // remount). If the slot holds a non-chat ephemeral (or none), fall through to
    // openEphemeral, which replaces it or adds a fresh chat tab.
    const target = nonTerminalTarget(dock, opts.groupId)
    const targetGroupId = target?.direction === 'within' ? target.referenceGroup : undefined
    const reusable = targetGroupId
      ? dock.getGroup(targetGroupId)?.panels.find(
          (p) => {
            if (!ephemeralPanels.value[p.id] || panelComponentOf(p.id) !== 'chat') return false
            const existingSid = panelSessionId(p)
            return !isDeletedSessionForCurrentBot(existingSid)
          },
        )
      : undefined
    if (reusable) {
      reusable.api.updateParameters({ sessionId: sid, explicitSelection })
      reusable.api.setTitle(title)
      if (!activate) return
      setNextChatActivationExplicit(reusable.id, explicitSelection)
      focusPanel(reusable)
      // Repointing an already-active panel doesn't refire activation, so select
      // the session explicitly (idempotent if it was already active).
      selectChatSession(sid, explicitSelection)
      return
    }
    const id = nextChatPanelId(bid)
    setNextChatActivationExplicit(id, explicitSelection)
    openEphemeral({
      id,
      component: 'chat',
      title,
      params: { sessionId: sid, explicitSelection },
      groupId: opts.groupId,
      inactive: !activate,
    })
  }

  function openSessionChatFromView(opts: {
    viewId: string
    sessionId: string
    title?: string
    expectedSessionId?: string | null
    explicitSelection?: boolean
    activate?: boolean
  }) {
    const dock = api.value
    const origin = dock?.getPanel(opts.viewId)
    if (!dock || !origin || panelComponentOf(origin.id) !== 'chat') return
    if ('expectedSessionId' in opts && panelSessionId(origin) !== opts.expectedSessionId) return
    openSessionChat({
      sessionId: opts.sessionId,
      title: opts.title,
      groupId: origin.group.id,
      explicitSelection: opts.explicitSelection,
      groupLocal: true,
      activate: opts.activate,
    })
  }

  /** Open a session as a PINNED chat tab. This is the sidebar right-click "Open
   * in New Tab": a single click (openSessionChat) reuses the group's ephemeral
   * chat slot so browsing the sidebar swaps one preview tab, but an explicit
   * right-click means "give me a separate tab" — so it lands as a pinned chat
   * tab the preview slot can never evict. An already-open tab for the session is
   * promoted to pinned + focused. */
  function openSessionChatPinned(opts: { sessionId: string, title?: string, groupId?: string }) {
    const dock = api.value
    if (!dock) return
    const sid = opts.sessionId.trim()
    if (!sid) return
    const bid = (currentBotId.value ?? '').trim()
    if (!bid) return
    // Same guard as openSessionChat: a row mid-deletion can still be visible in
    // the sidebar, so refuse to (re)open a session being deleted for this bot.
    if (isDeletedSessionForCurrentBot(sid)) return
    const title = opts.title?.trim() || chatTitleFallbackFor(sid)
    const existing = chatPanelForSession(sid)
    if (existing) {
      existing.api.setTitle(title)
      pinPanel(existing.id)
      focusPanel(existing)
      return
    }
    const target = nonTerminalTarget(dock, opts.groupId)
    dock.addPanel({
      id: nextChatPanelId(bid),
      component: 'chat',
      title,
      params: { sessionId: sid, explicitSelection: true },
      renderer: 'always',
      ...(target ? { position: target } : {}),
    })
  }

  /** Open (or focus) a subagent session in the region right of the primary
   * conversation — the same geometry the live Desktop uses for GUI tools: the
   * chat stays on the left, and the first editor region to its right hosts the
   * spawned agents. The first subagent open splits that region off
   * horizontally; later opens join it as additional pinned tabs, so several
   * subagent sessions can sit side by side without expanding again. An
   * already-open tab for the session is focused wherever it lives. */
  function openSubagentSession(opts: { sessionId: string, title?: string }) {
    const dock = api.value
    if (!dock) return
    const sid = opts.sessionId.trim()
    if (!sid) return
    const bid = (currentBotId.value ?? '').trim()
    if (!bid) return
    if (isDeletedSessionForCurrentBot(sid)) return
    const title = opts.title?.trim() || chatTitleFallbackFor(sid)

    const existing = chatPanelForSession(sid)
    if (existing) {
      existing.api.setTitle(title)
      pinPanel(existing.id)
      focusPanel(existing)
      return
    }

    const primaryGroup = dock.groups.find(group => !isTerminalOnlyGroup(group))
    if (!primaryGroup) {
      openSessionChatPinned({ sessionId: sid, title })
      return
    }
    const adjacentRight = dock.adjacentGroupInDirection(primaryGroup, 'right')
    const secondaryGroup = adjacentRight && !isTerminalOnlyGroup(adjacentRight)
      ? adjacentRight
      : undefined
    dock.addPanel({
      id: nextChatPanelId(bid),
      component: 'chat',
      title,
      params: { sessionId: sid, explicitSelection: true },
      renderer: 'always',
      position: secondaryGroup
        ? { referenceGroup: secondaryGroup.id, direction: 'within' }
        : { referenceGroup: primaryGroup.id, direction: 'right' },
    })
  }

  /** Open or focus a draft chat tab (no session yet). An explicit group owns its
   * own draft; callers without a group retain the legacy global-first behavior. */
  function openDraftChat(opts?: { title?: string, groupId?: string, explicitSelection?: boolean }) {
    const dock = api.value
    if (!dock) return
    const bid = (currentBotId.value ?? '').trim()
    if (!bid) return
    // Public entry (sidebar New Session, /new, empty-dock fallback): the user
    // asked for a draft, so stop shielding the restored layout from mutations.
    suppressSelectionDockMutations = false
    const draftCandidates = opts?.groupId
      ? dock.getGroup(opts.groupId)?.panels ?? []
      : dock.panels
    const existingDraft = draftCandidates.find(
      p => panelComponentOf(p.id) === 'chat' && panelSessionId(p) === null,
    )
    if (existingDraft) {
      const explicitSelection = opts?.explicitSelection === true
      if (dock.activePanel?.id !== existingDraft.id) {
        setNextChatActivationExplicit(existingDraft.id, explicitSelection)
        focusPanel(existingDraft)
      }
      chatStore.selectDraft({ explicitSelection })
      return
    }
    const id = nextChatPanelId(bid)
    setNextChatActivationExplicit(id, opts?.explicitSelection === true)
    openEphemeral({
      id,
      component: 'chat',
      title: opts?.title?.trim() || DEFAULT_CHAT_TITLE,
      params: { sessionId: null, explicitSelection: opts?.explicitSelection === true },
      groupId: opts?.groupId,
    })
  }

  // Activating a chat tab focuses its panel-scoped state and mirrors its session
  // into the global selection. Gated to chat panels by the caller.
  function activateChatSession(
    panel: { id: string, params?: Record<string, unknown> },
    options?: { explicitSelection?: boolean },
  ) {
    // Dockview emits active-panel changes while fromJSON restores the saved layout.
    // That is not a user click, and must not promote a stale restored chat tab into
    // an explicit chat-selection entry before chat initialization/default ACP wins.
    if (suppressPersist) return
    // Switch the panel-scoped chat state before the global selection changes. ACP
    // draft staging uses this transition to persist the old view before loading
    // the newly focused panel.
    chatStore.focusChatView(panel.id)
    const explicitOverride = consumeNextChatActivationExplicit(panel.id)
    const sid = panelSessionId(panel)
    if (sid) {
      const panelExplicitSelection = typeof panel.params?.explicitSelection === 'boolean'
        ? panel.params.explicitSelection
        : true
      const explicitSelection = options?.explicitSelection
        ?? explicitOverride
        ?? panelExplicitSelection
      selectChatSession(sid, explicitSelection)
    }
    else {
      const explicitSelection = options?.explicitSelection
        ?? explicitOverride
        ?? (
          chatStore.hasExplicitSessionSelection === true
          || panel.params?.explicitSelection === true
        )
      chatStore.selectDraft({ explicitSelection })
    }
  }

  function syncRestoredChatSelection(options?: { preserveRestoredChat?: boolean }) {
    const dock = api.value
    const sid = (chatStore.sessionId ?? '').trim()
    if (!dock) return
    const explicitSelection = chatStore.hasExplicitSessionSelection === true
    // Cold-start guard: non-explicit selection must not invent dock tabs after
    // a non-empty restore. Explicit clicks bypass it. Separate from
    // preserveRestoredChat, which only covers the empty-selection + restored
    // real-session case below.
    const blockNonExplicitOpen = suppressSelectionDockMutations && !explicitSelection
    const active = dock.activePanel
    const activeIsChat = !!active && panelComponentOf(active.id) === 'chat'
    const activeSession = activeIsChat ? panelSessionId(active) : null
    const groupId = activeIsChat ? active.group.id : undefined
    if (sid) {
      // A non-explicit stored session may be the last auto-picked history item.
      // While chat initialization is still deciding whether default ACP should win,
      // do not let the restored layout promote that stale id into an explicit user
      // selection. Once loading settles, the loading watcher calls this again.
      if (!explicitSelection && chatStore.loadingChats) return
      if (isDeletedSessionForCurrentBot(sid)) return
      if (activeIsChat && activeSession === sid) {
        chatStore.focusChatView(active.id)
        return
      }
      const existing = chatPanelForSession(sid)
      if (existing) {
        setNextChatActivationExplicit(existing.id, explicitSelection)
        focusPanel(existing)
        return
      }
      // Auto-picked / cold-start selection must not add a chat tab on top of a
      // restored File/Preview (or any non-empty) workspace. If the restored
      // active panel is already a different chat, pull selection onto it so
      // Recents highlights the conversation on screen — not the auto-pick.
      if (blockNonExplicitOpen) {
        adoptActiveRestoredChatSelection(activeSession, active?.id)
        return
      }
      openSessionChat({ sessionId: sid, groupId, explicitSelection })
      return
    }
    if (!activeIsChat) return
    if (activeSession === null) {
      chatStore.focusChatView(active.id)
      if (active.params?.explicitSelection !== explicitSelection) {
        active.api.updateParameters({ explicitSelection })
      }
      chatStore.selectDraft({ explicitSelection })
      return
    }
    if (options?.preserveRestoredChat || blockNonExplicitOpen) {
      // A fresh top-level tab has no separately-seeded chat selection, but its
      // restored workspace can already contain the chat the user was viewing.
      // Keep that panel instead of interpreting the empty selection as a request
      // for a second draft. Align the global selection to the preserved panel
      // (non-explicit) so the Recents highlight matches the open conversation.
      adoptActiveRestoredChatSelection(activeSession, active.id)
      return
    }
    openDraftChat({ groupId, explicitSelection })
  }

  // Align global selection to the chat on screen (or to none). Called after a
  // non-empty restore blocks inventing tabs: if the active panel is a chat,
  // Recents must highlight that session; if there is no chat to adopt,
  // drop initialize()'s non-explicit auto-pick so Recents stays unselected.
  function adoptActiveRestoredChatSelection(sessionId: string | null, panelId?: string) {
    if (!sessionId || isDeletedSessionForCurrentBot(sessionId)) {
      if (!chatStore.hasExplicitSessionSelection && (chatStore.sessionId ?? '').trim()) {
        chatStore.resetToEmptyComposer({
          clearPendingACP: false,
          explicitSelection: false,
          draftIntent: false,
        })
      }
      return
    }
    if (panelId) chatStore.focusChatView(panelId)
    // Already pointing at the preserved panel — leave explicitness alone.
    if ((chatStore.sessionId ?? '').trim() === sessionId) return
    // Non-explicit: layout ownership, not a user click.
    selectChatSession(sessionId, false)
  }

  function syncDraftTargetFromState() {
    const dock = api.value
    if (!dock || suppressPersist) return
    if ((selection.sessionId ?? '').trim()) return
    if (chatStore.hasExplicitSessionSelection !== true && !chatStore.pendingACPSessionInput) return
    // Explicit empty-composer / ACP draft staging is a real request to show a
    // draft, even after a non-empty restore — clear the cold-start guard so
    // syncRestoredChatSelection can open one.
    if (suppressSelectionDockMutations) suppressSelectionDockMutations = false
    syncRestoredChatSelection()
  }

  function syncDraftChatExplicitSelection() {
    const dock = api.value
    if (!dock) return
    const explicitSelection = !chatStore.sessionId && chatStore.hasExplicitSessionSelection === true
    const panel = dock.activePanel
    if (!panel || panelComponentOf(panel.id) !== 'chat' || panelSessionId(panel) !== null) return
    if (panel.params?.explicitSelection === explicitSelection) return
    panel.api.updateParameters({ explicitSelection })
  }

  // Keep each open chat tab's title in step with its session's server title. Draft
  // tabs (and real sessions with no title yet) keep their caller-set i18n title, so
  // we only overwrite when the server has a non-empty title — guarded against
  // setTitle thrash on background reorders.
  function syncChatTitles() {
    const dock = api.value
    if (!dock) return
    for (const panel of dock.panels) {
      if (panelComponentOf(panel.id) !== 'chat') continue
      const sid = panelSessionId(panel)
      if (!sid) continue
      const session = chatStore.knownSessionSummary(sid)
      const title = (session?.title ?? '').trim()
      if (title && panel.api.title !== title) panel.api.setTitle(title)
    }
  }

  // Drop chat tabs for sessions whose delete request has actually succeeded.
  // Do not infer this from the paginated sessions list: an open older tab may be
  // valid even when it is not present in the current sidebar page.
  function reconcileDeletedChatPanels(targetBotId?: string | null) {
    const dock = api.value
    const bid = (targetBotId ?? loadedBotId ?? currentBotId.value ?? '').trim()
    if (!dock || !bid) return
    if (!targetBotId && loadedBotId && loadedBotId !== bid) return
	    const deletedSessionIds = deletedSessionIdsByBot.get(bid)
	    if (!deletedSessionIds || deletedSessionIds.size === 0) return
	    const latestDeleted = chatStore.deletedSession
	    const resetComposerScope = latestDeleted?.botId === bid ? latestDeleted.composerScope?.trim() : ''
	    const closingPanels: Array<{ id: string }> = []
	    for (const panel of [...dock.panels]) {
	      if (panelComponentOf(panel.id) !== 'chat') continue
	      const sid = panelSessionId(panel)
	      if (!sid || !deletedSessionIds.has(sid)) continue
	      if (resetComposerScope && resetComposerScope === `${bid}:${panel.id}`) {
	        resetDeletedChatPanelToDraft(panel)
	        continue
	      }
	      reconcilingDeletedChatPanelIds.add(panel.id)
      closingPanels.push({ id: panel.id })
      holdDeletedChatActivation()
      panel.api.close()
    }
    if (closingPanels.length === 0) {
      releaseDeletedChatActivationNow()
      ensureSelectedChatPanel()
      return
    }
    for (const { id } of closingPanels) {
      void nextTick(() => {
        if (api.value?.getPanel(id)) {
          reconcilingDeletedChatPanelIds.delete(id)
          if (reconcilingDeletedChatPanelIds.size === 0) {
            releaseDeletedChatActivationAfterRemove()
          }
          return
        }
        reconcilingDeletedChatPanelIds.delete(id)
        if (reconcilingDeletedChatPanelIds.size === 0) {
          releaseDeletedChatActivationAfterRemove()
          void nextTick(() => ensureSelectedChatPanel())
        }
      })
    }
  }

  // Refill an EMPTY dock with the persistent "New Session" page. Guarded to the
  // empty dock (not merely "no chat tab"): the respawn opens into the group's
  // ephemeral slot, so firing while a file/preview tab is open would evict it.
  function ensureDraftChatPanel() {
    const dock = api.value
    if (!dock || suppressPersist || draftChatQueued) return
    if (dock.panels.length > 0) return
    if (!(currentBotId.value ?? '').trim()) return

    draftChatQueued = true
    void nextTick(() => {
      draftChatQueued = false
      const latestDock = api.value
      if (!latestDock || suppressPersist || latestDock.panels.length > 0) return
      if (!(currentBotId.value ?? '').trim()) return
      // Dock is empty again — cold-start shielding no longer applies.
      suppressSelectionDockMutations = false
      // Only an EXPLICIT selection may refill as that session. initialize()'s
      // auto-pick leaves a non-explicit sessionId even when we refused to open
      // its tab over a File/Preview restore; honoring it here would surface a
      // random Untitled Session the moment the user closes the last file tab.
      // Live tabs that already selected a session explicitly keep that refill.
      const sid = (chatStore.sessionId ?? '').trim()
      const explicitSelection = chatStore.hasExplicitSessionSelection === true
      if (sid && explicitSelection && !isDeletedSessionForCurrentBot(sid)) {
        openSessionChat({
          sessionId: sid,
          explicitSelection: true,
        })
      } else {
        openDraftChat({ explicitSelection })
      }
    })
  }

  // Mobile top bar "←": re-activate the chat panel for the CURRENT session.
  // Secondary panels are never closed by going back (renderer 'always' keeps
  // terminals/WebRTC alive), so back is a pure activation. Falls back to any
  // open chat tab — focusing it pulls the global selection onto it via the
  // activation sync — and on an empty dock to the draft respawn guard.
  function activateChatPanel() {
    const dock = api.value
    if (!dock) return
    const sid = (selection.sessionId ?? '').trim()
    if (sid && !isDeletedSessionForCurrentBot(sid)) {
      const current = chatPanelForSession(sid)
      if (current) {
        focusPanel(current)
        return
      }
    }
    const anyChat = dock.panels.find(p => panelComponentOf(p.id) === 'chat')
    if (anyChat) {
      focusPanel(anyChat)
      return
    }
    ensureDraftChatPanel()
  }

  function openFile(filePath: string) {
    if (!hasCurrentPermission('workspace_read')) return
    const path = (filePath ?? '').trim()
    if (!path) return
    openEphemeral({
      id: `file:${path}`,
      component: 'file',
      title: fileBaseName(path),
      params: { filePath: path },
    })
  }

  // Open a file as a PINNED tab in the active editor group. This is the
  // right-click "Open": a single click opens an ephemeral preview (replaced by
  // the next preview open), but an explicit right-click → Open means "keep this
  // one" — so it lands as a normal pinned tab that the preview slot can never
  // evict. Same group as a preview (no split — that is Open to the Side), it
  // just skips the ephemeral mark. A file already open is promoted to pinned +
  // focused rather than duplicated.
  function openFilePinned(filePath: string, groupId?: string) {
    if (!hasCurrentPermission('workspace_read')) return
    const path = (filePath ?? '').trim()
    if (!path) return
    const dock = api.value
    if (!dock) return
    const id = `file:${path}`
    const existing = dock.getPanel(id)
    if (existing) {
      pinPanel(existing.id)
      focusPanel(existing)
      return
    }
    const target = nonTerminalTarget(dock, groupId)
    dock.addPanel({
      id,
      component: 'file',
      title: fileBaseName(path),
      params: { filePath: path },
      renderer: 'always',
      ...(target ? { position: target } : {}),
    })
  }

  // "Open to the Side": show a file as a PINNED panel in a group to the RIGHT of
  // the active editor group. dockview enforces ONE panel per id (a file can't be
  // opened twice), so this is an ACTION not a duplicate-open: if the file is
  // already open anywhere, the existing panel is MOVED to a fresh right-side group
  // (api.moveTo) rather than focused in place — so right-clicking "Open to the
  // Side" always lands the file beside its neighbour, even when it was sitting in
  // the active group as a tab. Pinned from the start (never in ephemeralPanels) so
  // the preview slot can't evict it. A brand-new file is added split-right.
  function openFileToSide(filePath: string, groupId?: string) {
    if (!hasCurrentPermission('workspace_read')) return
    const path = (filePath ?? '').trim()
    if (!path) return
    // Mobile has no "side": degrade to a pinned tab in the single stack.
    if (isMobile.value) {
      openFilePinned(path, groupId)
      return
    }
    const dock = api.value
    if (!dock) return
    const id = `file:${path}`
    // Anchor the split off an EDITOR group, never the terminal strip (matches
    // openFilePinned's terminal-exclusion).
    const anchor = nonTerminalAnchorGroup(dock, groupId)
    const existing = dock.getPanel(id)
    if (existing) {
      pinPanel(existing.id)
      // Already alone in its own group → it's effectively "to the side" already,
      // so just focus it instead of moving (which would nest another group and
      // shuffle the layout on every repeat). Otherwise pull it out beside the
      // anchor (or its own group when there is no editor anchor to split from).
      const alreadyAlone = existing.group.panels.length === 1
      if (!alreadyAlone) {
        existing.api.moveTo({ group: anchor ?? existing.group, position: 'right' })
      }
      focusPanel(existing)
      return
    }
    // New file: split RIGHT off the editor anchor. When there is no editor group
    // to split from (empty dock, or only terminal groups), fall back to
    // nonTerminalTarget so the file lands in its OWN group ('above' the terminal
    // strip) instead of defaulting into the active terminal group — matches
    // openFilePinned's terminal-exclusion.
    const position = anchor
      ? { referenceGroup: anchor.id, direction: 'right' as const }
      : nonTerminalTarget(dock, groupId)
    dock.addPanel({
      id,
      component: 'file',
      title: fileBaseName(path),
      params: { filePath: path },
      renderer: 'always',
      ...(position ? { position } : {}),
    })
  }

  // VS Code's "Open Preview to the Side": render a markdown/html file's preview
  // as its own panel in a group beside the source editor, instead of toggling the
  // source view in place. Focuses an existing preview for the same file.
  function openPreview(filePath: string, title?: string, groupId?: string) {
    if (!hasCurrentPermission('workspace_read')) return
    const path = (filePath ?? '').trim()
    if (!path) return
    const dock = api.value
    if (!dock) return
    const id = `preview:${path}`
    const existing = dock.getPanel(id)
    if (existing) {
      focusPanel(existing)
      return
    }
    // A preview is the side equivalent of the editor's ephemeral slot: keep ONE
    // preview to the side. If an ephemeral preview already exists, replace it in
    // its own group; otherwise split to the right of the source editor's group
    // (the preview action lives in that group's header), falling back to active.
    const prevPreview = dock.panels.find(
      p => ephemeralPanels.value[p.id] && panelComponentOf(p.id) === 'preview',
    )
    const position = prevPreview
      ? { referenceGroup: prevPreview.group.id, direction: 'within' as const }
      : (() => {
          // Mobile forbids the side-by-side preview: join the stack as a tab.
          if (isMobile.value) return nonTerminalTarget(dock, groupId)
          const referenceGroup = groupId || dock.activeGroup?.id
          return referenceGroup
            ? { referenceGroup, direction: 'right' as const }
            : undefined
        })()
    dock.addPanel({
      id,
      component: 'preview',
      title: title || fileBaseName(path),
      params: { filePath: path },
      renderer: 'always',
      ...(position ? { position } : {}),
    })
    markEphemeral(id)
    if (prevPreview && prevPreview.id !== id) prevPreview.api.close()
  }

  // Open or focus a tab that renders a message attachment (a stored media asset).
  // Unlike openFile/openPreview this is NOT a workspace path: the tab re-resolves
  // its source from the content hash (or falls back to a direct URL), so it works
  // for the user's own uploads without a workspace_read grant. Lands in the editor
  // area like a file tab; refocuses an existing tab for the same asset.
  function openAsset(args: OpenAssetPreviewArgs) {
    const key = (args.key ?? '').trim()
    if (!key) return
    openEphemeral({
      id: `asset:${key}`,
      component: 'asset',
      title: args.name || 'file',
      params: {
        name: args.name,
        botId: args.botId,
        contentHash: args.contentHash,
        src: args.src,
      },
    })
  }

  function openTerminal(groupId?: string) {
    if (!hasCurrentPermission('workspace_exec')) return
    const bid = (currentBotId.value ?? '').trim()
    const state = ensureBotLayout(bid)
    const dock = api.value
    if (!state || !dock) return
    // Mobile hides the tab strip — the only UI enumerating open panels — and ←
    // only re-activates chat, so an always-new terminal would strand the
    // previous one alive but unreachable and unclosable, leaking a shell per
    // tap. Focus the existing terminal instead (same focus-existing semantics
    // as Browser/Display); a second terminal requires closing the first.
    if (isMobile.value) {
      const existing = dock.panels.find(panel => panel.id.startsWith('terminal:'))
      if (existing) {
        focusPanel(existing)
        return
      }
    }
    const next = state.terminalCounter + 1
    patchBotLayout(bid, { terminalCounter: next })
    // The "+" lives per group. Fired from a terminal-only group, the new session
    // JOINS it (another tab in the bottom panel). Fired from an editor group's
    // "+" menu — or opened programmatically — it lands in the bottom terminal
    // panel, which opens directly below the INITIATING editor column when none
    // exists yet (see defaultTerminalPosition). Passing a non-terminal groupId
    // straight through would wrongly merge the terminal into that editor group.
    const initiating = groupId ? dock.getGroup(groupId) : undefined
    const joinTerminalGroup = !!initiating && isTerminalOnlyGroup(initiating)
    const id = `terminal:${next}`
    addTerminalPanel({
      id,
      title: terminalTitleFallback(id),
      groupId: joinTerminalGroup ? groupId : undefined,
      position: joinTerminalGroup ? undefined : defaultTerminalPosition(dock, groupId),
    })
  }

  function openBrowser(groupId?: string) {
    if (!hasCurrentPermission('manage')) return
    const dock = api.value
    // Mobile hides the tab strip, so an always-new browser would strand the
    // previous panel alive but unreachable after returning to chat. Match the
    // terminal/display mobile behavior: focus the existing browser first.
    if (isMobile.value && dock) {
      const existing = dock.panels.find(panel => panel.id.startsWith('browser:'))
      if (existing) {
        focusPanel(existing)
        return
      }
    }
    addBrowserPanel(DEFAULT_BROWSER_ADDRESS, undefined, groupId)
  }

  // Create a fresh browser panel at `address`. Shared by the manual "+ New
  // Browser" entry (always-new, default "Browser N" title) and the link-click
  // path (after a dedup miss, titled with the address). Returns false when there
  // is no dock/bot layout.
  function addBrowserPanel(address: string, title: string | undefined, groupId?: string): boolean {
    const bid = (currentBotId.value ?? '').trim()
    const state = ensureBotLayout(bid)
    if (!state || !api.value) return false
    const next = state.browserCounter + 1
    patchBotLayout(bid, { browserCounter: next })
    focusOrAdd({
      id: `browser:${next}`,
      component: 'browser',
      title: title ?? `Browser ${next}`,
      params: { address },
      groupId,
    })
    return true
  }

  // Open the workspace browser panel at a specific local address (e.g. from a
  // clicked localhost link in chat markdown or terminal output). If a browser
  // tab already shows the exact same normalized URL, focus it instead of opening
  // a duplicate. Returns true when a tab was opened or focused, false when the
  // browser is unavailable (no permission / no dock / unparseable address) so
  // callers can fall back to the OS browser.
  function openBrowserAt(address: string, groupId?: string): boolean {
    if (!hasCurrentPermission('manage')) return false
    const dock = api.value
    if (!dock) return false
    let target: string
    try {
      target = parseBrowserAddress(address).display
    } catch {
      return false
    }
    const existing = dock.panels.find((panel) => {
      if (!panel.id.startsWith('browser:')) return false
      const current = (panel.params as { address?: string } | undefined)?.address
      if (!current) return false
      try {
        return parseBrowserAddress(current).display === target
      } catch {
        return false
      }
    })
    if (existing) {
      focusPanel(existing)
      return true
    }
    return addBrowserPanel(target, target, groupId)
  }

  function focusExistingDisplayPanel(dock: DockviewApi): boolean {
    const panels = dock.panels.filter(panel => panel.id.startsWith('display:'))
    if (!panels.length) return false
    focusPanel(panels[0]!)
    for (const extra of panels.slice(1)) {
      extra.api.close()
    }
    return true
  }

  function openDisplay(groupId?: string) {
    if (!hasCurrentPermission('manage')) return
    const dock = api.value
    if (!dock) return
    if (focusExistingDisplayPanel(dock)) return
    focusOrAdd({
      id: DISPLAY_PANEL_ID,
      component: 'display',
      title: i18n.global.t('chat.display.title'),
      groupId,
    })
  }

  // GUI tools should keep the conversation visible on the left and reserve the
  // first region to its right for the live Desktop. Bottom terminal groups do
  // not count as editor regions, and a below-only split must not be mistaken for
  // the requested right-side region.
  function openDisplayForAgentUse() {
    if (!hasCurrentPermission('manage')) return
    const dock = api.value
    if (!dock) return
    // Mobile never auto-opens the Desktop for agent activity. The single
    // stack has no right-side region, so open/focus steals the WHOLE screen
    // from the conversation — and the runtime fires one request per GUI tool
    // call, so a single turn would rip the user back to the viewer over and
    // over (issue #1071). Watching stays possible via the manual top-bar
    // "+" → Desktop entry; the viewer connects on demand there.
    if (isMobile.value) return

    const primaryGroup = dock.groups.find(group => !isTerminalOnlyGroup(group))
    if (!primaryGroup) {
      openDisplay()
      return
    }

    const adjacentRight = dock.adjacentGroupInDirection(primaryGroup, 'right')
    const secondaryGroup = adjacentRight && !isTerminalOnlyGroup(adjacentRight)
      ? adjacentRight
      : undefined
    const existingDisplays = dock.panels.filter(panel => panelComponentOf(panel.id) === 'display')
    const existing = existingDisplays[0]
    let targetDisplay = existing

    if (secondaryGroup) {
      const displayInSecondary = secondaryGroup.panels.find(
        panel => panelComponentOf(panel.id) === 'display',
      )
      if (displayInSecondary) {
        targetDisplay = displayInSecondary
        focusPanel(displayInSecondary)
      }
      else if (existing) {
        existing.api.moveTo({ group: secondaryGroup, position: 'center' })
        focusPanel(existing)
      }
      else {
        dock.addPanel({
          id: DISPLAY_PANEL_ID,
          component: 'display',
          title: i18n.global.t('chat.display.title'),
          renderer: 'always',
          position: { referenceGroup: secondaryGroup.id, direction: 'within' },
        })
      }
    }
    else if (existing) {
      // Desktop is a singleton. When it is still a tab in the only editor
      // region, move that live panel to a new right split instead of reconnecting
      // a duplicate viewer.
      if (existing.group.id !== primaryGroup.id || primaryGroup.panels.length > 1) {
        existing.api.moveTo({ group: primaryGroup, position: 'right' })
      }
      focusPanel(existing)
    }
    else {
      dock.addPanel({
        id: DISPLAY_PANEL_ID,
        component: 'display',
        title: i18n.global.t('chat.display.title'),
        renderer: 'always',
        position: { referenceGroup: primaryGroup.id, direction: 'right' },
      })
    }

    for (const extra of existingDisplays) {
      if (extra !== targetDisplay) extra.api.close()
    }
  }

  function uniqueSplitPanelId(baseId: string): string {
    const dock = api.value
    if (!dock) return baseId
    if (!dock.getPanel(baseId)) return baseId
    let n = 2
    while (dock.getPanel(`${baseId}~${n}`)) n++
    return `${baseId}~${n}`
  }

  /** Split the group's active tab into a new pane beside/below it (VS Code-style).
   * Duplicates the current panel into the new split rather than moving it. */
  function splitGroup(groupId: string, direction: 'right' | 'below') {
    const dock = api.value
    if (!dock) return
    const group = dock.getGroup(groupId)
    const source = group?.activePanel
    if (!source || !group) return

    const comp = panelComponentOf(source.id)
    if (!comp) return

    // Mobile forbids splits: duplicate into the SAME group as a tab instead.
    const position = isMobile.value
      ? { referenceGroup: groupId, direction: 'within' as const }
      : { referenceGroup: groupId, direction }
    const title = source.title ?? source.api.title ?? ''
    const params = source.params as Record<string, unknown> | undefined

    switch (comp) {
      case 'chat': {
        const bid = (currentBotId.value ?? '').trim()
        if (!bid) return
        const id = nextChatPanelId(bid)
        const explicitSelection = typeof params?.explicitSelection === 'boolean'
          ? params.explicitSelection
          : chatStore.hasExplicitSessionSelection === true
        setNextChatActivationExplicit(id, explicitSelection)
        dock.addPanel({
          id,
          component: 'chat',
          title,
          params: {
            sessionId: params?.sessionId ?? null,
            explicitSelection,
          },
          renderer: 'always',
          position,
        })
        // Each split starts as this new group's preview slot. Choosing another
        // session in that group repoints the split in place until the user sends.
        markEphemeral(id)
        break
      }
      case 'terminal': {
        if (!hasCurrentPermission('workspace_exec')) return
        const bid = (currentBotId.value ?? '').trim()
        const state = ensureBotLayout(bid)
        if (!state) return
        const next = state.terminalCounter + 1
        patchBotLayout(bid, { terminalCounter: next })
        const id = `terminal:${next}`
        addTerminalPanel({
          id,
          title: terminalTitleFallback(id),
          position,
        })
        break
      }
      case 'file':
      case 'preview': {
        if (!hasCurrentPermission('workspace_read')) return
        dock.addPanel({
          id: uniqueSplitPanelId(source.id),
          component: comp,
          title,
          params: params ? { ...params } : undefined,
          renderer: 'always',
          position,
        })
        break
      }
      case 'browser': {
        if (!hasCurrentPermission('manage')) return
        const bid = (currentBotId.value ?? '').trim()
        const state = ensureBotLayout(bid)
        if (!state) return
        const next = state.browserCounter + 1
        patchBotLayout(bid, { browserCounter: next })
        dock.addPanel({
          id: `browser:${next}`,
          component: 'browser',
          title: title || `Browser ${next}`,
          params: { address: (params?.address as string | undefined) ?? DEFAULT_BROWSER_ADDRESS },
          renderer: 'always',
          position,
        })
        break
      }
      case 'display': {
        if (!hasCurrentPermission('manage')) return
        if (focusExistingDisplayPanel(dock)) return
        openDisplay(group.id)
        break
      }
      case 'schedule': {
        if (!hasCurrentPermission('manage')) return
        dock.addPanel({
          id: uniqueSplitPanelId(source.id),
          component: 'schedule',
          title,
          params: params ? { ...params } : undefined,
          renderer: 'always',
          position,
        })
        break
      }
      case 'asset': {
        dock.addPanel({
          id: uniqueSplitPanelId(source.id),
          component: 'asset',
          title,
          params: params ? { ...params } : undefined,
          renderer: 'always',
          position,
        })
        break
      }
    }
  }

  function openSchedule(scheduleId?: string, title?: string, groupId?: string) {
    if (!hasCurrentPermission('manage')) return
    const bid = (currentBotId.value ?? '').trim()
    if (!bid) return
    const panelTitle = title?.trim() || 'Schedule'
    const state = ensureBotLayout(bid)
    if (!state) return
    const panelId = scheduleId
      ? `schedule:${scheduleId}`
      : `schedule:new:${state.scheduleCounter + 1}`
    const panel = api.value?.getPanel(panelId)
    if (panel) {
      panel.api.setTitle(panelTitle)
      focusPanel(panel)
      return
    }
    if (!scheduleId) {
      patchBotLayout(bid, { scheduleCounter: state.scheduleCounter + 1 })
    }
    focusOrAdd({
      id: panelId,
      component: 'schedule',
      title: panelTitle,
      params: { scheduleId },
      groupId,
    })
  }

  // Don't let the sole remaining tab close if it's an empty draft chat — there is
  // nothing to gain by closing then respawning a draft. A real-session chat tab
  // CAN close (the dock then respawns a draft via ensureDraftChatPanel).
  function shouldKeepLastDraftChat(id: string) {
    const dock = api.value
    if (!dock || dock.panels.length !== 1) return false
    const panel = dock.getPanel(id)
    if (!panel) return false
    return panelComponentOf(id) === 'chat' && panelSessionId(panel) === null
  }

  function closeTab(id: string) {
    if (shouldKeepLastDraftChat(id)) return
    const panel = api.value?.getPanel(id)
    panel?.api.close()
  }

  function setFileDirty(panelId: string, dirty: boolean) {
    if (!!fileDirty.value[panelId] === dirty) return
    const next = { ...fileDirty.value }
    if (dirty) next[panelId] = true
    else delete next[panelId]
    fileDirty.value = next
  }

  function registerFileSaveHandler(panelId: string, handler: () => Promise<boolean>) {
    saveHandlers.set(panelId, handler)
  }

  function unregisterFileSaveHandler(panelId: string) {
    saveHandlers.delete(panelId)
  }

  // Drop every trace of a panel once it's gone, so a stale dirty flag can't keep
  // a phantom count on the sidebar badge or a phantom entry in the close queue.
  function cleanupPanelState(id: string) {
    if (fileDirty.value[id]) {
      const next = { ...fileDirty.value }
      delete next[id]
      fileDirty.value = next
    }
    if (ephemeralPanels.value[id]) {
      const next = { ...ephemeralPanels.value }
      delete next[id]
      ephemeralPanels.value = next
    }
    saveHandlers.delete(id)
    if (closeQueue.value.includes(id)) {
      closeQueue.value = closeQueue.value.filter(q => q !== id)
    }
  }

  const dirtyFileCount = computed(
    () => Object.values(fileDirty.value).filter(Boolean).length,
  )

  function tabBaseName(id: string): string {
    if (id.startsWith('file:')) return fileBaseName(id.slice('file:'.length))
    if (id.startsWith('preview:')) return fileBaseName(id.slice('preview:'.length))
    return api.value?.getPanel(id)?.api.title ?? id
  }

  // The tab the close-confirm dialog is currently asking about (head of queue).
  const pendingClose = computed<{ panelId: string, title: string } | null>(() => {
    const id = closeQueue.value[0]
    return id ? { panelId: id, title: tabBaseName(id) } : null
  })

  // User-initiated close. Clean tabs close at once; a dirty file is queued for
  // the close-confirm dialog instead of vanishing with its unsaved edits.
  function requestCloseTab(id: string) {
    if (!fileDirty.value[id]) {
      closeTab(id)
      return
    }
    if (!closeQueue.value.includes(id)) {
      closeQueue.value = [...closeQueue.value, id]
    }
  }

  // Batch close (close-others / close-all): clean tabs go immediately, dirty ones
  // queue up and the dialog walks them one at a time.
  function requestCloseTabs(ids: string[]) {
    const queued = [...closeQueue.value]
    for (const id of ids) {
      if (fileDirty.value[id]) {
        if (!queued.includes(id)) queued.push(id)
      } else {
        closeTab(id)
      }
    }
    closeQueue.value = queued
  }

  async function resolvePendingClose(action: 'save' | 'discard' | 'cancel') {
    const id = closeQueue.value[0]
    if (!id) return
    if (action === 'cancel') {
      // Cancel aborts the whole pending batch — matches VS Code.
      closeQueue.value = []
      return
    }
    if (action === 'save') {
      const handler = saveHandlers.get(id)
      const ok = handler ? await handler() : true
      // On save failure leave the tab open (the viewer surfaced the error); just
      // drop it from the queue so the dialog can move to the next one.
      if (ok) closeTab(id)
    } else {
      closeTab(id)
    }
    closeQueue.value = closeQueue.value.filter(q => q !== id)
  }

  function updateBrowserAddress(panelId: string, address: string) {
    const panel = api.value?.getPanel(panelId)
    if (!panel) return
    panel.api.updateParameters({ address })
    if (address) {
      panel.api.setTitle(address)
    }
  }

  function isPanelAllowed(id: string): boolean {
    switch (panelComponentOf(id)) {
      case 'chat':
        return true
      case 'file':
      case 'preview':
        return hasCurrentPermission('workspace_read')
      case 'asset':
        // A message attachment is the user's own content, not a workspace file —
        // viewing it never requires the workspace_read grant.
        return true
      case 'terminal':
        return hasCurrentPermission('workspace_exec')
      case 'browser':
      case 'display':
      case 'schedule':
        return hasCurrentPermission('manage')
      default:
        return false
    }
  }

  function prunePanels() {
    const dock = api.value
    if (!dock) return
    const perms = currentBot.value?.current_user_permissions
    if (!perms || perms.length === 0) return
    for (const panel of [...dock.panels]) {
      if (!isPanelAllowed(panel.id)) {
        panel.api.close()
      }
    }
  }

  // ---- sidebar (activity bar + side panel) ---------------------------------

  const sidebarView = useLocalStorage<SidebarView>('workspace-sidebar-view', 'sessions')
  const sidebarOpen = useLocalStorage('workspace-sidebar-open', true)
  const sidebarWidth = useLocalStorage('workspace-sidebar-width', 256)
  const workbenchOpen = useLocalStorage('workspace-workbench-open', true)

  // Mobile main-navigation sheet. Deliberately NOT persisted and deliberately
  // NOT one of the four desktop workbench keys above: navigation is a
  // transient overlay, and the desktop rail preference must stay untouched by
  // anything the mobile shell does.
  const mobileNavOpen = ref(false)

  function openMobileNav() {
    mobileNavOpen.value = true
  }

  function closeMobileNav() {
    mobileNavOpen.value = false
  }


  // Push/pull model (see main-section + sidebar): the rail is in flow and slides
  // out to the left (margin-left) while the dock, a flex sibling, grows to fill
  // the space — content shifts. Toggling just flips this boolean; the rail
  // animates its margin on a shared curve and dockview relays out per frame as
  // the dock width changes, so the right-side actions ("+") stay pinned (right
  // edge never moves) instead of snapping.
  function setWorkbench(open: boolean) {
    workbenchOpen.value = open
  }

  // Horizontal nav only switches the active view and ensures the sidebar is
  // shown. Collapsing the whole sidebar lives on the workbench toggle (the
  // chrome button over the dock), not on the nav items.
  function selectSidebarView(view: SidebarView) {
    sidebarView.value = view
    sidebarOpen.value = true
    setWorkbench(true)
  }

  function toggleWorkbench() {
    setWorkbench(!workbenchOpen.value)
  }

  function showWorkbench() {
    setWorkbench(true)
  }

  function hideWorkbench() {
    setWorkbench(false)
  }

  // ---- bottom panel actions -------------------------------------------------
  // Terminals use the standard dockview layout but always default to a group
  // at the bottom of the editor area, so the layout feels like a VS Code-style
  // bottom panel while remaining fully draggable and composable.

  // groupId is the header strip that triggered the "+" (the terminal group's own
  // bar). Passing it keeps the new session in THAT group; without it (editor "+"
  // menu) the terminal routes to the default bottom slot.
  function openTerminalInPanel(groupId?: string) {
    openTerminal(groupId)
  }

  // One-shot navigation request consumed by the sidebar files panel.
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

  // ---- lifecycle -----------------------------------------------------------

  function resetBot(botId: string) {
    const bid = (botId ?? '').trim()
    if (!bid) return
    const next = { ...storage.value }
    delete next[bid]
    storage.value = next
    if (loadedBotId === bid && api.value) {
      suppressPersist = true
      try {
        api.value.clear()
      } finally {
        suppressPersist = false
      }
    }
    void nextTick(() => clearTerminalSnapshotsForBot(bid))
  }

  function resetAll() {
    storage.value = {}
    pendingFilesPath.value = null
    if (api.value) {
      suppressPersist = true
      try {
        api.value.clear()
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
    ensureBotLayout(next)
    if (api.value && loadedBotId !== next) {
      if (deletedSessionIdsByBot.get(next)?.size) holdDeletedChatActivation()
      restoreLayout(next)
      reconcileDeletedChatPanels()
    }
  }, { immediate: true })

  // Crossing the breakpoint either way re-shapes the dock. Entering mobile
  // collapses any multi-group arrangement (only reachable by narrowing a
  // desktop window — a phone session is single-group from cold start); live
  // panels survive the merge, terminals/WebRTC included. Leaving mobile
  // restores the tab strips but deliberately does NOT rebuild the dock from
  // the persisted snapshot: a rebuild would go through onDidRemovePanel,
  // which deletes terminal snapshots — killing live shells just because a
  // window widened. Instead the merged stack stays up with persistence sealed
  // (dockHoldsMobileStack, armed on entry), and the desktop arrangement comes
  // back from the untouched persisted snapshot on the next bot load, not from
  // memory. The nav sheet is a mobile-only overlay, so its state resets on
  // exit.
  watch(isMobile, (mobile) => {
    if (!mobile) {
      mobileNavOpen.value = false
    }
    const dock = api.value
    if (!dock) return
    if (mobile) {
      // From here the dock diverges from the persisted desktop layout (the
      // merge now, mobile-only opens later) with no desktop restore to
      // re-sync it — seal persistence until one happens.
      dockHoldsMobileStack = true
      mergeGroupsIntoSingle(dock)
    }
    syncMobileGroupHeaders(dock)
  })

  watch(
    () => [currentBotId.value, ...(currentBot.value?.current_user_permissions ?? [])].join('|'),
    () => prunePanels(),
  )

  // A sent message pins exactly the originating chat view (it is no longer a
  // preview). Draft promotion also repoints only that view to the new session.
  watch(() => chatStore.userSentInSession, (sig) => {
    if (!sig) return
    const dock = api.value
    const bid = (currentBotId.value ?? '').trim()
    if (!dock || !bid || sig.botId !== bid) return
    const panel = dock.getPanel(sig.viewId)
    if (!panel || panelComponentOf(panel.id) !== 'chat') return
    const currentSessionId = panelSessionId(panel)
    if (sig.wasDraft) {
      // A delayed create must not overwrite a panel that has since been reused
      // for another session.
      if (currentSessionId && currentSessionId !== sig.id) return
      panel.api.updateParameters({ sessionId: sig.id })
    }
    else if (currentSessionId !== sig.id) return
    pinPanel(panel.id)
    syncChatTitles()
  })

  watch(() => chatStore.guiToolUseRequested, (request) => {
    if (!request || request.botId !== (currentBotId.value ?? '').trim()) return
    openDisplayForAgentUse()
  }, { flush: 'sync' })

  // `/new` can finish resolving Agent defaults after focus has moved to a
  // different split. The workspace owns the final Draft panel id because a
  // preview can be reused in place while a pinned source must remain open and
  // receive a new preview beside it.
  watch(() => chatStore.draftViewRequested, (request) => {
    if (!request) return
    const dock = api.value
    const bid = (currentBotId.value ?? '').trim()
    if (!dock || request.botId !== bid) return
    const panel = dock.getPanel(request.viewId)
    if (!panel || panelComponentOf(panel.id) !== 'chat') return
    if (panelSessionId(panel) !== request.expectedSessionId) return

    const activate = request.activate && dock.activePanel?.id === panel.id
    if (ephemeralPanels.value[panel.id]) {
      chatStore.applyDraftViewRequest(request, activate)
      panel.api.updateParameters({
        sessionId: null,
        explicitSelection: request.explicitSelection,
      })
      panel.api.setTitle(DEFAULT_CHAT_TITLE)
      return
    }

    const id = nextChatPanelId(bid)
    const draftRequest = { ...request, viewId: id }
    if (activate) setNextChatActivationExplicit(id, request.explicitSelection)
    if (!openEphemeral({
      id,
      component: 'chat',
      title: DEFAULT_CHAT_TITLE,
      params: {
        sessionId: null,
        explicitSelection: request.explicitSelection,
      },
      groupId: panel.group.id,
      inactive: !activate,
    })) return
    chatStore.applyDraftViewRequest(draftRequest, activate)
  }, { flush: 'sync' })

  // A Fork belongs to the panel where it was requested, even if hydration
  // completes after another split gains focus. Preview sources are repointed;
  // pinned sources keep their Session and get a new preview in the same group.
  watch(() => chatStore.forkedSessionRequested, (request) => {
    if (!request) return
    const dock = api.value
    const bid = (currentBotId.value ?? '').trim()
    if (!dock || request.botId !== bid) return
    const panel = dock.getPanel(request.viewId)
    if (!panel || panelComponentOf(panel.id) !== 'chat') return
    if (panelSessionId(panel) !== request.expectedSessionId) return
    openSessionChatFromView({
      viewId: request.viewId,
      sessionId: request.sessionId,
      title: request.title,
      expectedSessionId: request.expectedSessionId,
      explicitSelection: request.explicitSelection,
      activate: request.activate && dock.activePanel?.id === panel.id,
    })
  }, { flush: 'sync' })

  // Keep the active chat tab in step with the global session when it is set from
  // OUTSIDE a tab activation (initialize picking a session, an ACP session being
  // created, a session deleted). Declared AFTER the userSentInSession watch so a
  // send-promotion has already repointed the draft tab by the time this runs —
  // chatPanelForSession then finds it and this just focuses (no duplicate tab).
  watch(() => selection.sessionId, (sid) => {
    const dock = api.value
    if (!dock || suppressPersist) return
    const trimmed = (sid ?? '').trim()
    if (!trimmed) {
      syncDraftTargetFromState()
      return
    }
    if (isDeletedSessionForCurrentBot(trimmed)) return
    const existing = chatPanelForSession(trimmed)
    const explicitSelection = chatStore.hasExplicitSessionSelection === true
    if (existing) {
      setNextChatActivationExplicit(existing.id, explicitSelection)
      focusPanel(existing)
      return
    }
    // initialize() auto-picks the latest history item with explicitSelection
    // false. That must not open a chat tab over a restored File/Preview layout.
    // Sidebar clicks go through openSessionChat directly (or set explicit).
    if (suppressSelectionDockMutations && !explicitSelection) return
    // No tab yet: open one. If the group's ephemeral slot is a draft, this
    // repoints it in place (no stray draft tab); otherwise it adds a chat tab.
    openSessionChat({
      sessionId: trimmed,
      explicitSelection,
    })
  })

  watch(
    () => chatStore.pendingACPSessionInput,
    (pending) => {
      if (!pending) return
      syncDraftTargetFromState()
    },
  )

  watch(
    () => `${chatStore.sessionId ?? ''}:${chatStore.hasExplicitSessionSelection === true ? '1' : '0'}`,
    () => syncDraftChatExplicitSelection(),
  )

  // Server renames flow into each open chat tab's title. Keyed by a sorted
  // id:title digest so it fires on title changes, not on every sidebar reorder.
  watch(
    () => chatStore.knownSessions.map(s => `${s.id}:${s.title ?? ''}`).sort().join('|'),
    () => syncChatTitles(),
  )

  watch(() => chatStore.deletedSession, (deleted) => {
    if (!deleted) return
    const deletedIds = deletedSessionIdsByBot.get(deleted.botId) ?? new Set<string>()
    deletedIds.add(deleted.id)
    deletedSessionIdsByBot.set(deleted.botId, deletedIds)
    reconcileDeletedChatPanels(deleted.botId)
  })

  watch(
    () => [chatStore.loadingChats, chatStore.sessions.length, currentBotId.value] as const,
    () => {
      if (chatStore.loadingChats) return
      // After chats load, reconcile selection ↔ dock. suppressSelectionDockMutations
      // inside syncRestoredChatSelection still blocks auto-picked opens on a
      // restored non-empty workspace.
      syncRestoredChatSelection({
        preserveRestoredChat: suppressSelectionDockMutations,
      })
    },
  )

  return {
    api,
    activeId,
    isMobile,
    activePanelIsChat,
    mobileNavOpen,
    panelDragging,
    fileDirty,
    ephemeralPanels,
    pinPanel,
    dirtyFileCount,
    pendingClose,
    sidebarView,
    sidebarOpen,
    workbenchOpen,
    sidebarWidth,
    pendingFilesPath,
    registerApi,
    releaseApi,
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
    openTerminalInPanel,
    openBrowser,
    openBrowserAt,
    openDisplay,
    splitGroup,
    openSchedule,
    closeTab,
    requestCloseTab,
    requestCloseTabs,
    resolvePendingClose,
    setFileDirty,
    registerFileSaveHandler,
    unregisterFileSaveHandler,
    updateBrowserAddress,
    updateTerminalTitle,
    resetBot,
    resetAll,
  }
})
