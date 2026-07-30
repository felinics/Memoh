// @vitest-environment jsdom

import { createApp, defineComponent, h, nextTick, onMounted, ref } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

const dock = {
  panels: [
    { id: 'file:/data/AGENTS.md' },
    { id: 'preview:/data/AGENTS.md' },
  ],
  layout: vi.fn(),
}
const api = ref<typeof dock | null>(null)
const workspaceStore = {
  api,
  panelDragging: false,
  registerApi: vi.fn((nextApi: typeof dock) => {
    api.value = nextApi
  }),
  requestCloseTab: vi.fn(),
  requestCloseTabs: vi.fn(),
  releaseApi: vi.fn(),
  openSessionChat: vi.fn(),
  openDraftChat: vi.fn(),
}

vi.mock('dockview-vue', () => ({
  DockviewVue: defineComponent({
    emits: ['ready'],
    setup(_, { emit }) {
      onMounted(() => emit('ready', { api: dock }))
      return () => h('div')
    },
  }),
}))

vi.mock('@/store/workspace-tabs', () => ({
  useWorkspaceTabsStore: () => workspaceStore,
}))

vi.mock('@/store/chat-list', () => ({
  useChatStore: () => ({
    currentBotId: 'bot-1',
    sessionId: null,
    hasExplicitSessionSelection: false,
  }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('./dockview/panel-chat.vue', () => ({ default: { render: () => null } }))
vi.mock('./dockview/panel-file.vue', () => ({ default: { render: () => null } }))
vi.mock('./dockview/panel-preview.vue', () => ({ default: { render: () => null } }))
vi.mock('./dockview/panel-asset.vue', () => ({ default: { render: () => null } }))
vi.mock('./dockview/panel-terminal.vue', () => ({ default: { render: () => null } }))
vi.mock('./dockview/panel-browser.vue', () => ({ default: { render: () => null } }))
vi.mock('./dockview/panel-display.vue', () => ({ default: { render: () => null } }))
vi.mock('./dockview/panel-schedule.vue', () => ({ default: { render: () => null } }))
vi.mock('./dockview/workspace-watermark.vue', () => ({ default: { render: () => null } }))
vi.mock('./dockview/workspace-tab-host.vue', () => ({ default: { render: () => null } }))
vi.mock('./dockview/terminal-tab.vue', () => ({ default: { render: () => null } }))
vi.mock('./dockview/group-actions.vue', () => ({ default: { render: () => null } }))
vi.mock('./dockview/header-add-actions.vue', () => ({ default: { render: () => null } }))
vi.mock('./dockview/prefix-header-actions.vue', () => ({ default: { render: () => null } }))
vi.mock('./dockview/tab-close-confirm.vue', () => ({ default: { render: () => null } }))

class ResizeObserverMock {
  observe() {}
  disconnect() {}
}

Object.defineProperty(globalThis, 'ResizeObserver', {
  value: ResizeObserverMock,
  configurable: true,
})

describe('chat workspace initialization', () => {
  let root: HTMLElement | null = null

  afterEach(() => {
    workspaceStore.registerApi.mockClear()
    workspaceStore.openSessionChat.mockClear()
    workspaceStore.openDraftChat.mockClear()
    workspaceStore.releaseApi.mockClear()
    dock.layout.mockClear()
    api.value = null
    root?.remove()
    root = null
  })

  it('leaves a restored non-empty workspace unchanged when it has no chat panel', async () => {
    // The repository's test tsconfig does not load Vue's ambient module shim.
    // @ts-expect-error Vitest's Vue plugin resolves this SFC at runtime.
    const ChatWorkspace = (await import('./chat-workspace.vue')).default
    root = document.createElement('div')
    Object.defineProperties(root, {
      clientWidth: { value: 1200 },
      clientHeight: { value: 800 },
    })
    document.body.appendChild(root)

    const app = createApp(ChatWorkspace)
    app.mount(root)
    await nextTick()

    expect(workspaceStore.registerApi).toHaveBeenCalledWith(dock)
    expect(workspaceStore.openSessionChat).not.toHaveBeenCalled()
    expect(workspaceStore.openDraftChat).not.toHaveBeenCalled()

    app.unmount()
  })
})
