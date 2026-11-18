// @vitest-environment jsdom
import { createApp, defineComponent, h, inject, nextTick, ref } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { openInFileManagerKey } from '../composables/useFileManagerProvider'
import { createEmptyWorkbenchState, insertTab } from '@/workbench/tree'

const initialState = insertTab(
  insertTab(
    createEmptyWorkbenchState(),
    { id: 'file:/data/AGENTS.md', pinned: false, target: { kind: 'file', filePath: '/data/AGENTS.md' } },
  ).state,
  { id: 'preview:/data/AGENTS.md', pinned: false, target: { kind: 'preview', filePath: '/data/AGENTS.md' } },
).state

const workspaceStore = {
  workbench: initialState,
  isMobile: false,
  panelDragging: false,
  pendingClose: ref(null),
  mountWorkspace: vi.fn(),
  unmountWorkspace: vi.fn(),
  openSessionChat: vi.fn(),
  openDraftChat: vi.fn(),
  openFilePinned: vi.fn(),
  openFilesAt: vi.fn(),
  openAsset: vi.fn(),
  resolvePendingClose: vi.fn(),
}

vi.mock('@/store/workspace-tabs', () => ({
  useWorkspaceTabsStore: () => workspaceStore,
}))

vi.mock('@felinic/ui', () => ({
  Button: 'button',
  Dialog: 'div',
  DialogContent: 'div',
  DialogDescription: 'div',
  DialogFooter: 'div',
  DialogHeader: 'div',
  DialogTitle: 'div',
  PanePlaceholder: 'div',
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('./workbench/workbench-tree.vue', () => ({
  default: defineComponent({
    props: ['node'],
    emits: ['slotMounted', 'slotUnmounted'],
    setup() {
      const openFile = inject(openInFileManagerKey)!
      return () => h('div', [
        h('button', { class: 'open-workspace-file', onClick: () => openFile('/data/a.md') }, 'Open file'),
        h('button', { class: 'open-outside-file', onClick: () => openFile('/home/user/project/package.json') }, 'Open outside file'),
      ])
    },
  }),
}))

vi.mock('./workbench/workbench-stage.vue', () => ({
  default: defineComponent({
    props: ['slotMap', 'slotVersion'],
    setup: () => () => h('div'),
  }),
}))

describe('chat workspace initialization', () => {
  let root: HTMLElement | null = null

  afterEach(() => {
    workspaceStore.mountWorkspace.mockClear()
    workspaceStore.unmountWorkspace.mockClear()
    workspaceStore.openSessionChat.mockClear()
    workspaceStore.openDraftChat.mockClear()
    workspaceStore.openFilePinned.mockClear()
    root?.remove()
    root = null
  })

  it('mounts the workbench store without forcing a chat tab onto a non-empty file workspace and forwards file links cleanly', async () => {
    const ChatWorkspace = (await import('./chat-workspace.vue')).default
    root = document.createElement('div')
    document.body.appendChild(root)

    const app = createApp(ChatWorkspace)
    app.mount(root)
    await nextTick()

    expect(workspaceStore.mountWorkspace).toHaveBeenCalledOnce()
    expect(workspaceStore.openSessionChat).not.toHaveBeenCalled()
    expect(workspaceStore.openDraftChat).not.toHaveBeenCalled()

    root.querySelector<HTMLButtonElement>('.open-workspace-file')!.click()
    expect(workspaceStore.openFilePinned).toHaveBeenCalledWith('/data/a.md')

    root.querySelector<HTMLButtonElement>('.open-outside-file')!.click()
    expect(workspaceStore.openFilePinned).toHaveBeenCalledWith('/home/user/project/package.json')

    app.unmount()
    expect(workspaceStore.unmountWorkspace).toHaveBeenCalledOnce()
  })
})
