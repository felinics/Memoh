/* eslint-disable vue/one-component-per-file */
// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import { createPinia, defineStore, setActivePinia } from 'pinia'
import i18n from '@/i18n'
import { useChatSelectionStore } from '@/store/chat-selection'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { listGroups } from '@/workbench/tree'
import type { WorkbenchGroupNode } from '@/workbench/types'

if (typeof globalThis.ResizeObserver === 'undefined') {
  globalThis.ResizeObserver = class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

vi.mock('@/composables/useEnvironment', () => ({
  useCompactShell: () => ref(false),
  useCoarsePointer: () => ref(false),
}))

vi.mock('@/composables/useTerminalCache', () => ({
  clearTerminalSnapshots: vi.fn(),
  clearTerminalSnapshotsForBot: vi.fn(),
  deleteTerminalSnapshot: vi.fn(),
  terminalCacheKey: (b: string, p: string) => `${b}::${p}`,
}))

vi.mock('./workbench-prefix-nav.vue', () => ({
  default: defineComponent({
    setup: () => () => h('div', { 'data-workbench-prefix-nav': '' }),
  }),
}))

const passThrough = defineComponent({
  inheritAttrs: false,
  setup: (_, { slots }) => () => slots.default?.(),
})

vi.mock('@felinic/ui', () => ({
  Button: defineComponent({
    setup: (_, { attrs, slots }) => () => h('button', { type: 'button', ...attrs }, slots.default?.()),
  }),
  ContextMenu: passThrough,
  ContextMenuTrigger: passThrough,
  ContextMenuContent: defineComponent({ setup: () => () => null }),
  ContextMenuItem: defineComponent({ setup: () => () => null }),
  ContextMenuSeparator: defineComponent({ setup: () => () => null }),
  DropdownMenu: passThrough,
  DropdownMenuTrigger: passThrough,
  DropdownMenuContent: defineComponent({ setup: () => () => null }),
  DropdownMenuItem: defineComponent({ setup: () => () => null }),
  DropdownMenuSeparator: defineComponent({ setup: () => () => null }),
}))

const useMockChatStore = defineStore('tab-bar-test-chat', () => ({
  currentBotId: ref<string | null>('bot-1'),
  bots: ref([{ id: 'bot-1', current_user_permissions: ['manage', 'workspace_exec', 'workspace_read'] }]),
  sessionId: ref<string | null>(null),
  hasExplicitSessionSelection: ref(false),
  loadingChats: ref(false),
  sessions: ref([]),
  knownSessions: ref([]),
  deletedSession: ref(null),
  draftViewRequested: ref(null),
  forkedSessionRequested: ref(null),
  userSentInSession: ref(null),
  draftPromoted: ref(null),
  focusChatView: vi.fn(),
  selectSession: vi.fn(),
  selectDraft: vi.fn(),
  resetToEmptyComposer: vi.fn(),
  knownSessionSummary: () => null,
  isSessionTentative: () => false,
  applyDraftViewRequest: vi.fn(),
}))

vi.mock('@/store/chat-list', () => ({
  useChatStore: () => useMockChatStore(),
}))

describe('WorkbenchTabBar keyboard, ARIA, drag-region, and scroll behavior', () => {
  let host: HTMLDivElement | null = null
  let app: ReturnType<typeof createApp> | null = null
  let pinia = createPinia()

  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
    vi.clearAllMocks()
    pinia = createPinia()
    setActivePinia(pinia)
    useChatSelectionStore().setBot('bot-1')
  })

  afterEach(() => {
    app?.unmount()
    app = null
    host?.remove()
    host = null
  })

  it('lets Enter/Space on focused close button reach the button without being intercepted, exposes tablist ARIA, gates top-edge drag region, and scrolls active tab into view', async () => {
    const WorkbenchTabBar = (await import('./workbench-tab-bar.vue')).default
    const store = useWorkspaceTabsStore()
    store.mountWorkspace()
    store.openFilePinned('/data/a.ts')
    store.openFilePinned('/data/b.ts')
    store.splitGroup(store.activeGroupId!, 'below')

    const groups = listGroups(store.workbench.root)
    expect(groups).toHaveLength(2)
    const [topGroup, bottomGroup] = groups as [WorkbenchGroupNode, WorkbenchGroupNode]

    host = document.createElement('div')
    document.body.appendChild(host)
    app = createApp({
      setup: () => () => {
        const currentGroups = listGroups(store.workbench.root)
        return h('div', currentGroups.map(g => h(WorkbenchTabBar, { key: g.id, group: g })))
      },
    })
    app.use(pinia).use(i18n)
    app.mount(host)
    await nextTick()

    // 1. ARIA tablist -> tab hierarchy
    const tablists = host.querySelectorAll('[role="tablist"]')
    expect(tablists.length).toBeGreaterThanOrEqual(2)
    expect(tablists[0]!.querySelectorAll('[role="tab"]').length).toBeGreaterThan(0)

    // 2. Top-edge group void is window-draggable; bottom split group void is no-drag.
    const topHeader = host.querySelector<HTMLElement>(`[data-workbench-header-group-id="${topGroup.id}"]`)!
    const bottomHeader = host.querySelector<HTMLElement>(`[data-workbench-header-group-id="${bottomGroup.id}"]`)!
    const topVoid = topHeader.querySelector<HTMLElement>('[data-workbench-drag-void]')!
    const bottomVoid = bottomHeader.querySelector<HTMLElement>('[data-workbench-drag-void]')!
    expect(topVoid.className).toContain('[-webkit-app-region:drag]')
    expect(bottomVoid.className).toContain('[-webkit-app-region:no-drag]')

    // 3. Keyboard Enter on focused close button must NOT be preventDefault()'d by .workbench-tab-hit's keydown handler.
    const tabAEl = topHeader.querySelector<HTMLElement>('[data-workbench-tab-id="file:/data/a.ts"]')!
    const closeBtnA = tabAEl.querySelector<HTMLButtonElement>('[data-workbench-tab-close]')!
    const tabAHit = tabAEl.querySelector<HTMLElement>('.workbench-tab-hit')!

    const enterOnClose = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    closeBtnA.dispatchEvent(enterOnClose)
    expect(enterOnClose.defaultPrevented).toBe(false)

    const spaceOnClose = new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true })
    closeBtnA.dispatchEvent(spaceOnClose)
    expect(spaceOnClose.defaultPrevented).toBe(false)

    // Pressing Enter directly on .workbench-tab-hit DOES preventDefault and activates the tab.
    store.activateTab('file:/data/b.ts')
    await nextTick()
    expect(store.activeId).toBe('file:/data/b.ts')
    const enterOnHit = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    tabAHit.dispatchEvent(enterOnHit)
    expect(enterOnHit.defaultPrevented).toBe(true)
    expect(store.activeId).toBe('file:/data/a.ts')
    await nextTick()

    // 4. Activating a tab calls scrollIntoView({ block: 'nearest', inline: 'nearest' }) on that tab element.
    const tabBEl = topHeader.querySelector<HTMLElement>('[data-workbench-tab-id="file:/data/b.ts"]')!
    const scrollIntoViewSpy = vi.fn()
    tabBEl.scrollIntoView = scrollIntoViewSpy
    store.activateTab('file:/data/b.ts')
    await nextTick()
    await nextTick()
    expect(scrollIntoViewSpy).toHaveBeenCalledWith({ block: 'nearest', inline: 'nearest' })

    // 5. Keyboard-activated close (detail: 0) closes the tab without engaging pointer consecutive-close width lock.
    const scroller = topHeader.querySelector<HTMLElement>('[data-workbench-tab-scroller]')!
    tabBEl.getBoundingClientRect = () => new DOMRect(0, 0, 150, 40)
    const closeBtnB = tabBEl.querySelector<HTMLButtonElement>('[data-workbench-tab-close]')!
    closeBtnB.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, detail: 0 }))
    await nextTick()
    expect(store.workbench.tabs['file:/data/b.ts']).toBeUndefined()
    expect(scroller.dataset.widthLocked).toBeUndefined()
  })
})
