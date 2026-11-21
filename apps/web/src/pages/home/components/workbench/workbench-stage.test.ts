/* eslint-disable vue/one-component-per-file, vue/require-prop-types */
// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import { createPinia, defineStore, setActivePinia } from 'pinia'
import i18n from '@/i18n'
import { useChatSelectionStore } from '@/store/chat-selection'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { createEmptyWorkbenchState, insertTab, splitGroupWithTab } from '@/workbench/tree'

const childCalls = vi.hoisted(() => ({
  chatMounts: [] as Array<{ botId?: string, tabId: string, sessionId: string | null }>,
  chatUnmounts: [] as Array<{ botId?: string, tabId: string }>,
  bindChatView: vi.fn(),
  unbindChatView: vi.fn(),
  focusChatView: vi.fn(),
  scheduleFetch: vi.fn(async () => ({ data: { id: 'sched', name: 'Job' } })),
  terminalMounts: [] as Array<{ botId: string, tabId: string }>,
  browserMounts: [] as Array<{ botId: string, tabId: string }>,
  displayFullscreenEmitter: { trigger: null as null | ((v: boolean) => void) },
}))

if (typeof globalThis.ResizeObserver === 'undefined') {
  globalThis.ResizeObserver = class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

vi.mock('vue-router', () => ({
  useRoute: () => ({ name: 'bot' }),
}))

vi.mock('@/lib/idle-prefetch', () => ({
  whenNetworkQuiet: () => new Promise(() => {}),
  prefetchLater: vi.fn(),
}))

vi.mock('@/composables/useIsMobile', () => ({
  useIsMobile: () => ref(false),
}))

vi.mock('@/composables/useTerminalCache', () => ({
  clearTerminalSnapshots: vi.fn(),
  clearTerminalSnapshotsForBot: vi.fn(),
  deleteTerminalSnapshot: vi.fn(),
  terminalCacheKey: (b: string, p: string) => `${b}::${p}`,
}))

vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdScheduleById: childCalls.scheduleFetch,
  deleteBotsByBotIdScheduleById: vi.fn(),
}))

vi.mock('@felinic/ui', () => ({
  Spinner: { template: '<span />' },
  ConfirmDeleteDialog: { template: '<div />' },
  InlineLoadingRow: { template: '<div />' },
  PanePlaceholder: 'div',
  Button: 'button',
  toast: { success: vi.fn(), error: vi.fn() },
}))

vi.mock('./view-file.vue', () => ({
  __esModule: true,
  default: defineComponent({
    props: ['botId', 'tabId', 'filePath', 'visible'],
    setup: props => () => h('div', { 'data-mock-view-file': `${props.botId}:${props.tabId}` }),
  }),
}))

vi.mock('@/pages/bots/components/schedule-editor.vue', () => ({
  __esModule: true,
  default: defineComponent({
    props: ['botId', 'mode', 'schedule'],
    setup: props => () => h('div', { 'data-schedule-editor-bot': props.botId }),
  }),
}))

vi.mock('../chat-pane.vue', () => ({
  __esModule: true,
  default: defineComponent({
    props: ['botId', 'tabId', 'sessionId', 'visible', 'active'],
    setup(props) {
      childCalls.chatMounts.push({ botId: props.botId, tabId: props.tabId, sessionId: props.sessionId })
      return () => h('div', {
        'data-mock-chat-pane': `${props.botId}:${props.tabId}:${props.sessionId ?? 'draft'}`,
      })
    },
    beforeUnmount() {
      childCalls.chatUnmounts.push({ botId: this.botId, tabId: this.tabId })
    },
  }),
}))

vi.mock('../terminal-pane.vue', () => ({
  __esModule: true,
  default: defineComponent({
    props: ['botId', 'tabId', 'active'],
    setup(props) {
      childCalls.terminalMounts.push({ botId: props.botId, tabId: props.tabId })
      return () => h('div', { 'data-mock-terminal': `${props.botId}:${props.tabId}` })
    },
  }),
}))

vi.mock('../browser-pane.vue', () => ({
  __esModule: true,
  default: defineComponent({
    props: ['botId', 'tabId', 'address', 'active'],
    setup(props) {
      childCalls.browserMounts.push({ botId: props.botId, tabId: props.tabId })
      return () => h('div', { 'data-mock-browser': `${props.botId}:${props.tabId}` })
    },
  }),
}))

vi.mock('../display-pane.vue', () => ({
  __esModule: true,
  default: defineComponent({
    props: ['botId', 'tabId', 'title', 'active'],
    emits: ['close', 'snapshot', 'fullscreenChange'],
    setup(props, { emit }) {
      childCalls.displayFullscreenEmitter.trigger = active => emit('fullscreenChange', active)
      return () => h('div', { 'data-mock-display': `${props.botId}:${props.tabId}` })
    },
  }),
}))

const useMockChatStore = defineStore('chat', () => ({
  currentBotId: ref<string | null>('bot-a'),
  bots: ref([
    { id: 'bot-a', current_user_permissions: ['manage', 'workspace_exec', 'workspace_read'] },
    { id: 'bot-b', current_user_permissions: ['manage', 'workspace_exec', 'workspace_read'] },
  ]),
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
  bindChatView: childCalls.bindChatView,
  unbindChatView: childCalls.unbindChatView,
  focusChatView: childCalls.focusChatView,
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

vi.mock('@/store/display-snapshots', () => ({
  useDisplaySnapshotsStore: () => ({ upsert: vi.fn() }),
}))

describe('WorkbenchStage cross-bot isolation and stacking', () => {
  let host: HTMLDivElement | null = null
  let app: ReturnType<typeof createApp> | null = null
  let pinia = createPinia()

  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
    vi.clearAllMocks()
    childCalls.chatMounts.length = 0
    childCalls.chatUnmounts.length = 0
    childCalls.terminalMounts.length = 0
    childCalls.browserMounts.length = 0
    childCalls.displayFullscreenEmitter.trigger = null
    pinia = createPinia()
    setActivePinia(pinia)
    const sel = useChatSelectionStore()
    sel.setBot('bot-a')

    // When selection store changes bot, mirror into chat store as real chat-list does.
    useMockChatStore().currentBotId = sel.currentBotId ?? 'bot-a'
  })

  afterEach(() => {
    app?.unmount()
    app = null
    host?.remove()
    host = null
  })

  it('keys outer stage wrappers by loadedBotId, isolates child pre-flush watchers, resets revealedTabs, and passes expectedBotId on unbind', async () => {
    let wbA = createEmptyWorkbenchState('g1')
    wbA = insertTab(wbA, { id: 'chat:1', pinned: false, target: { kind: 'chat', sessionId: 'sid-a', explicitSelection: true } })
    wbA = insertTab(wbA, { id: 'browser:1', pinned: false, target: { kind: 'browser', instance: 1, address: 'http://a' } })
    wbA = insertTab(wbA, { id: 'schedule:sched-a', pinned: false, target: { kind: 'schedule', scheduleId: 'sched-a' } })
    wbA = { ...wbA, root: { ...wbA.root, activeTabId: 'chat:1' } as typeof wbA.root }

    let wbB = createEmptyWorkbenchState('g1')
    // Active tab on bot-b is chat:1; terminal:1 and schedule:sched-b sit in the background.
    wbB = insertTab(wbB, { id: 'terminal:1', pinned: false, target: { kind: 'terminal', instance: 1 } })
    wbB = insertTab(wbB, { id: 'browser:1', pinned: false, target: { kind: 'browser', instance: 1, address: 'http://b' } })
    wbB = insertTab(wbB, { id: 'schedule:sched-b', pinned: false, target: { kind: 'schedule', scheduleId: 'sched-b' } })
    wbB = insertTab(wbB, { id: 'chat:1', pinned: false, target: { kind: 'chat', sessionId: 'sid-b', explicitSelection: true } })
    wbB = { ...wbB, root: { ...wbB.root, activeTabId: 'chat:1' } as typeof wbB.root }

    localStorage.setItem('memoh:workbench:v1', JSON.stringify({
      'bot-a': { workbench: wbA, terminalCounter: 0, browserCounter: 1, scheduleCounter: 0, chatCounter: 1 },
      'bot-b': { workbench: wbB, terminalCounter: 1, browserCounter: 1, scheduleCounter: 0, chatCounter: 1 },
    }))

    const WorkbenchStage = (await import('./workbench-stage.vue')).default
    const tabsStore = useWorkspaceTabsStore()
    tabsStore.mountWorkspace()

    host = document.createElement('div')
    document.body.appendChild(host)
    app = createApp({
      setup: () => () => h(WorkbenchStage),
    })
    app.use(pinia).use(i18n)
    app.mount(host)
    await nextTick()
    await vi.dynamicImportSettled()
    await nextTick()

    // Reveal schedule:sched-a on bot-a so ViewSchedule is mounted and watching.
    tabsStore.activateTab('schedule:sched-a')
    await nextTick()
    await vi.dynamicImportSettled()
    await nextTick()
    expect(childCalls.scheduleFetch).toHaveBeenCalledWith({
      path: { bot_id: 'bot-a', id: 'sched-a' },
      throwOnError: true,
    })
    childCalls.scheduleFetch.mockClear()

    // Switch to bot-b (both selection store and chat store update).
    useChatSelectionStore().setBot('bot-b')
    useMockChatStore().currentBotId = 'bot-b'
    await nextTick()
    await Promise.resolve()
    await vi.dynamicImportSettled()
    await nextTick()

    // No pre-flush leak: bot-b must NEVER query bot-a's schedule-a, nor bind (bot-b, sid-a)!
    for (const call of childCalls.scheduleFetch.mock.calls) {
      const arg = (call as Array<{ path: { bot_id: string, id: string } }>)[0]!
      expect(`${arg.path.bot_id}:${arg.path.id}`).not.toBe('bot-b:sched-a')
    }
    for (const call of childCalls.bindChatView.mock.calls) {
      const [, target] = call as [string, { botId: string, sessionId: string | null }]
      expect(`${target.botId}:${target.sessionId}`).not.toBe('bot-b:sid-a')
    }

    // Old ViewChat from bot-a unmounted and passed expectedBotId === 'bot-a' to unbindChatView.
    expect(childCalls.unbindChatView).toHaveBeenCalledWith('chat:1', 'bot-a')

    // BrowserPane on bot-b mounted a fresh instance for bot-b rather than reusing bot-a's iframe instance.
    expect(childCalls.browserMounts.map(m => `${m.botId}:${m.tabId}`)).toEqual([
      'bot-a:browser:1',
      'bot-b:browser:1',
    ])

    // Background terminal:1 on bot-b was NOT eagerly revealed by bot-a's state.
    expect(childCalls.terminalMounts).toEqual([])
  })

  it('elevates the DisplayPane panel wrapper to z-(--z-top) only while fullscreen is active', async () => {
    let wb = createEmptyWorkbenchState('g1')
    wb = insertTab(wb, { id: 'display:1', pinned: false, target: { kind: 'display' } }, { groupId: 'g1' })
    wb = splitGroupWithTab(wb, 'g1', 'right', { id: 'file:/data/a.ts', pinned: false, target: { kind: 'file', filePath: '/data/a.ts' } })
    localStorage.setItem('memoh:workbench:v1', JSON.stringify({
      'bot-a': { workbench: wb, terminalCounter: 0, browserCounter: 0, scheduleCounter: 0, chatCounter: 0 },
    }))

    const WorkbenchStage = (await import('./workbench-stage.vue')).default
    const tabsStore = useWorkspaceTabsStore()
    tabsStore.mountWorkspace()

    host = document.createElement('div')
    document.body.appendChild(host)
    app = createApp({
      setup: () => () => h(WorkbenchStage),
    })
    app.use(pinia).use(i18n)
    app.mount(host)
    await nextTick()
    await vi.dynamicImportSettled()
    await nextTick()

    const displayWrapper = host.querySelector<HTMLElement>('[data-workbench-panel-id="display:1"]')!
    expect(displayWrapper).not.toBeNull()
    expect(displayWrapper.classList.contains('z-(--z-top)')).toBe(false)

    childCalls.displayFullscreenEmitter.trigger?.(true)
    await nextTick()
    expect(displayWrapper.classList.contains('z-(--z-top)')).toBe(true)

    childCalls.displayFullscreenEmitter.trigger?.(false)
    await nextTick()
    expect(displayWrapper.classList.contains('z-(--z-top)')).toBe(false)
  })

  it('reveals Bot B active terminal immediately on bot switch and unscales slot rects under CSS entry scale', async () => {
    let wbA = createEmptyWorkbenchState('g1')
    wbA = insertTab(wbA, { id: 'terminal:1', pinned: false, target: { kind: 'terminal', instance: 1 } }, { groupId: 'g1' })
    let wbB = createEmptyWorkbenchState('g2')
    wbB = insertTab(wbB, { id: 'terminal:1', pinned: false, target: { kind: 'terminal', instance: 1 } }, { groupId: 'g2' })

    localStorage.setItem('memoh:workbench:v1', JSON.stringify({
      'bot-a': { workbench: wbA, terminalCounter: 1, browserCounter: 0, scheduleCounter: 0, chatCounter: 0 },
      'bot-b': { workbench: wbB, terminalCounter: 1, browserCounter: 0, scheduleCounter: 0, chatCounter: 0 },
    }))

    const WorkbenchStage = (await import('./workbench-stage.vue')).default
    const tabsStore = useWorkspaceTabsStore()
    tabsStore.mountWorkspace()

    host = document.createElement('div')
    document.body.appendChild(host)

    app = createApp({
      setup: () => () => h('div', [
        h('div', { 'data-workbench-slot-group-id': tabsStore.activeGroupId ?? 'g1' }),
        h(WorkbenchStage),
      ]),
    })
    app.use(pinia).use(i18n)
    app.mount(host)

    // Simulate ancestor CSS scale-[1.15] entry transition: layout clientWidth=1000×800, visual rect=1150×920, header=40px (46px visual).
    const wrapper = host.firstElementChild as HTMLElement
    const slotEl = wrapper.children[0] as HTMLElement
    const stageEl = wrapper.children[1] as HTMLElement
    slotEl.getBoundingClientRect = () => new DOMRect(0, 46, 1150, 874)
    Object.defineProperty(stageEl, 'clientWidth', { value: 1000, configurable: true })
    Object.defineProperty(stageEl, 'clientHeight', { value: 800, configurable: true })
    stageEl.getBoundingClientRect = () => new DOMRect(0, 0, 1150, 920)

    await nextTick()
    await vi.dynamicImportSettled()
    await nextTick()
    expect(childCalls.terminalMounts.map(m => `${m.botId}:${m.tabId}`)).toEqual(['bot-a:terminal:1'])

    // Switch to bot-b whose active tab is ALSO terminal:1 -> must mount immediately and compute unscaled 1000×760 box.
    useChatSelectionStore().setBot('bot-b')
    useMockChatStore().currentBotId = 'bot-b'
    await nextTick()
    await Promise.resolve()
    await vi.dynamicImportSettled()
    await nextTick()

    expect(childCalls.terminalMounts.map(m => `${m.botId}:${m.tabId}`)).toEqual([
      'bot-a:terminal:1',
      'bot-b:terminal:1',
    ])
    const termWrapper = host.querySelector<HTMLElement>('[data-workbench-panel-id="terminal:1"]')!
    expect(termWrapper.style.top).toBe('40px')
    expect(termWrapper.style.width).toBe('1000px')
    expect(termWrapper.style.height).toBe('760px')
  })

  it('activates an inactive split panel on capture-phase pointerdown and resets display fullscreen across bot switches', async () => {
    let wbA = createEmptyWorkbenchState('g1')
    wbA = insertTab(wbA, { id: 'display:1', pinned: false, target: { kind: 'display' } }, { groupId: 'g1' })
    wbA = splitGroupWithTab(wbA, 'g1', 'right', { id: 'file:/data/a.ts', pinned: false, target: { kind: 'file', filePath: '/data/a.ts' } })

    let wbB = createEmptyWorkbenchState('g2')
    wbB = insertTab(wbB, { id: 'display:1', pinned: false, target: { kind: 'display' } }, { groupId: 'g2' })

    localStorage.setItem('memoh:workbench:v1', JSON.stringify({
      'bot-a': { workbench: wbA, terminalCounter: 0, browserCounter: 0, scheduleCounter: 0, chatCounter: 0 },
      'bot-b': { workbench: wbB, terminalCounter: 0, browserCounter: 0, scheduleCounter: 0, chatCounter: 0 },
    }))

    const WorkbenchStage = (await import('./workbench-stage.vue')).default
    const tabsStore = useWorkspaceTabsStore()
    tabsStore.mountWorkspace()

    host = document.createElement('div')
    document.body.appendChild(host)
    app = createApp({
      setup: () => () => h(WorkbenchStage),
    })
    app.use(pinia).use(i18n)
    app.mount(host)
    await nextTick()
    await vi.dynamicImportSettled()
    await nextTick()

    // File split is active; clicking an inner child of display:1 that calls stopPropagation() must still activate display:1.
    expect(tabsStore.activeId).toBe('file:/data/a.ts')
    const displayInner = host.querySelector<HTMLElement>('[data-mock-display="bot-a:display:1"]')!
    displayInner.addEventListener('pointerdown', e => e.stopPropagation())
    displayInner.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true }))
    expect(tabsStore.activeId).toBe('display:1')

    // Enter fullscreen on bot-a's display:1, then switch to bot-b which also has display:1 visible -> fullscreen must reset.
    childCalls.displayFullscreenEmitter.trigger?.(true)
    await nextTick()
    expect(host.querySelector<HTMLElement>('[data-workbench-panel-id="display:1"]')!.classList.contains('z-(--z-top)')).toBe(true)

    useChatSelectionStore().setBot('bot-b')
    useMockChatStore().currentBotId = 'bot-b'
    await nextTick()
    await Promise.resolve()
    await vi.dynamicImportSettled()
    await nextTick()

    expect(host.querySelector<HTMLElement>('[data-workbench-panel-id="display:1"]')!.classList.contains('z-(--z-top)')).toBe(false)
  })
})
