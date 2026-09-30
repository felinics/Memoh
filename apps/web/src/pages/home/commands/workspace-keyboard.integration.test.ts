// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref, type App } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import type { DockviewApi, DockviewPanelApi } from 'dockview-vue'
import PanelFile from '../components/dockview/panel-file.vue'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { KEYBOARD_REGISTRY } from '@/composables/useKeyboardCommand'
import { appKeyboardCommands, createKeyboardCommandRegistry } from '@/lib/keyboard-commands'
import { connectBrowserKeyboardShortcutsLive } from '@/lib/browser-keyboard-shortcuts'
import { keyboardBindings, selectWebBindings } from '@/lib/keyboard-bindings'
import { registerWorkspaceTabCommands } from './workspace-tab-commands'

const sdk = vi.hoisted(() => ({ read: vi.fn(), write: vi.fn() }))
vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdContainerFsRead: sdk.read,
  postBotsByBotIdContainerFsWrite: sdk.write,
  getBotsByBotIdContainerFs: vi.fn(),
  getBotsByBotIdContainerFsDownload: vi.fn(),
}))
vi.mock('@felinic/ui', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
  Button: 'button', DiffTitleBar: 'div', PanePlaceholder: 'div',
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/i18n', () => ({ default: { global: { t: (key: string) => key, locale: ref('en') } } }))
vi.mock('@/lib/api-client', () => ({ sdkApiUrl: vi.fn(), sdkAuthQuery: vi.fn() }))
vi.mock('@/composables/useIsMobile', () => ({ useIsMobile: () => ref(false) }))
vi.mock('@/store/chat-list', async () => {
  const { defineStore } = await import('pinia')
  return { useChatStore: defineStore('keyboard-chat-fixture', () => ({
    fsChangedAt: ref(0), currentBotId: ref('bot-a'),
    bots: ref([{ id: 'bot-a', current_user_permissions: ['workspace_write'] }]),
    affectsPath: () => false, fsEventForPath: () => null,
    sessions: ref([]), knownSessions: ref([]), sessionId: ref(null), loadingChats: ref(false),
    hasExplicitSessionSelection: ref(false), activeSession: ref(null),
    pendingExternalAgentSessionInput: ref(null), draftViewRequested: ref(null),
    forkedSessionRequested: ref(null), deletedSession: ref(null),
  })) }
})
vi.mock('../components/dockview/panel-breadcrumb.vue', () => ({ default: 'div' }))
vi.mock('@/components/monaco-editor/diff.vue', () => ({ default: 'div' }))
vi.mock('@/components/monaco-editor/index.vue', () => ({
  default: (props: { modelValue: string; 'onUpdate:modelValue': (value: string) => void }) => h('textarea', {
    value: props.modelValue,
    onInput: (event: Event) => props['onUpdate:modelValue']((event.target as HTMLTextAreaElement).value),
  }),
}))

let app: App | undefined
let host: HTMLDivElement | undefined
let disconnect = () => {}
let store: ReturnType<typeof useWorkspaceTabsStore>

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  setActivePinia(createPinia())
  sdk.read.mockResolvedValue({ data: { content: 'initial', revision: 'r0' } })
  sdk.write.mockResolvedValue({ data: { revision: 'r1' } })
  sdk.write.mockClear()
  store = useWorkspaceTabsStore()
})
afterEach(() => {
  disconnect()
  app?.unmount()
  host?.remove()
  store.releaseApi()
  store.$dispose()
})

function registerDock() {
  let onRemove: (panel: { id: string }) => void = () => {}
  const panels = ['/a.txt', '/b.txt'].map(path => ({
    id: `file:${path}`, component: 'file',
    api: {
      id: `file:${path}`, isVisible: true, isActive: false, isGroupActive: false,
      close: vi.fn(), title: path,
      onDidVisibilityChange: () => ({ dispose() {} }),
      onDidActiveChange: () => ({ dispose() {} }),
      onDidActiveGroupChange: () => ({ dispose() {} }),
    },
  }))
  for (const panel of panels) {
    panel.api.close.mockImplementation(() => {
      panels.splice(panels.indexOf(panel), 1)
      onRemove(panel)
    })
  }
  let onActive: (event: { panel: typeof panels[number] }) => void = () => {}
  const disposable = () => ({ dispose() {} })
  const dock = {
    panels, groups: [], activePanel: panels[0],
    getPanel: (id: string) => panels.find(panel => panel.id === id),
    onDidActivePanelChange: (cb: typeof onActive) => { onActive = cb; return disposable() },
    onDidLayoutChange: disposable,
    onDidRemovePanel: (cb: typeof onRemove) => { onRemove = cb; return disposable() },
    onWillDragPanel: disposable, onWillDragGroup: disposable,
    onWillShowOverlay: disposable, onWillDrop: disposable,
  }
  store.registerApi(dock as unknown as DockviewApi)
  function activate(index: number) {
    const panel = panels[index]!
    dock.activePanel = panel
    onActive({ panel })
  }
  activate(0)
  return { panels: [...panels], dock, activate }
}

async function mountSplit() {
  const dock = registerDock()
  const registry = createKeyboardCommandRegistry()
  host = document.createElement('div')
  document.body.append(host)
  app = createApp({ setup: () => () => h('div', dock.panels.map(panel => h(PanelFile, {
    params: {
      params: { filePath: panel.api.title },
      api: panel.api as unknown as DockviewPanelApi,
      containerApi: dock.dock as unknown as DockviewApi,
    },
  }))) }).provide(KEYBOARD_REGISTRY, registry)
  app.mount(host)
  disconnect = connectBrowserKeyboardShortcutsLive(registry, () => selectWebBindings(keyboardBindings))
  for (let i = 0; i < 4; i++) await nextTick()
  const editors = [...host.querySelectorAll('textarea')]
  expect(editors).toHaveLength(2)
  async function edit(index: number, text: string) {
    const editor = editors[index]!
    editor.value = text
    editor.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
  }
  async function save(index: number) {
    editors[index]!.focus()
    const event = new KeyboardEvent('keydown', {
      key: 's', ctrlKey: true, bubbles: true, cancelable: true,
    })
    editors[index]!.dispatchEvent(event)
    await nextTick()
    return event
  }
  return { ...dock, edit, save }
}

describe('workspace keyboard ownership', () => {
  it('saves only the focused split and follows a later focus change', async () => {
    const view = await mountSplit()
    await view.edit(0, 'A')
    await view.edit(1, 'B')
    view.activate(1)
    await nextTick()
    await view.save(1)
    expect(sdk.write.mock.calls.map(([request]) => request.body.path)).toEqual(['/b.txt'])
    expect(store.fileDirty['file:/a.txt']).toBe(true)

    view.activate(0)
    await nextTick()
    await view.save(0)
    expect(sdk.write.mock.calls.map(([request]) => request.body.path)).toEqual(['/b.txt', '/a.txt'])
  })

  it('does not save a dirty background split when the focused file is clean', async () => {
    const view = await mountSplit()
    await view.edit(1, 'B')
    await view.save(0)
    expect(sdk.write).not.toHaveBeenCalled()
    expect(store.fileDirty['file:/b.txt']).toBe(true)
  })

  it('consumes a second save press while the first write is pending', async () => {
    const view = await mountSplit()
    await view.edit(0, 'A')
    let finish!: (value: { data: { revision: string } }) => void
    sdk.write.mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
    expect((await view.save(0)).defaultPrevented).toBe(true)
    expect((await view.save(0)).defaultPrevented).toBe(true)
    expect(sdk.write).toHaveBeenCalledOnce()
    finish({ data: { revision: 'r1' } })
    await vi.waitFor(() => expect(store.dirtyFileCount).toBe(0))
  })

  it.each(['cancel', 'discard', 'save', 'failure'] as const)('protects dirty close through %s after focus moves', async (action) => {
    const view = await mountSplit()
    await view.edit(0, 'A')
    const registry = createKeyboardCommandRegistry()
    const unregister = registerWorkspaceTabCommands(registry, store)
    try {
      registry.dispatch(appKeyboardCommands.closeCurrentWorkspaceTab)
      registry.dispatch(appKeyboardCommands.closeCurrentWorkspaceTab)
      expect(view.panels[0]!.api.close).not.toHaveBeenCalled()
      expect(store.pendingClose?.panelId).toBe('file:/a.txt')
      view.activate(1)
      await nextTick()
      if (action === 'failure') sdk.write.mockRejectedValueOnce(new Error('write failed'))
      await store.resolvePendingClose(action === 'failure' ? 'save' : action)

      expect(view.panels[0]!.api.close).toHaveBeenCalledTimes(action === 'save' || action === 'discard' ? 1 : 0)
      expect(view.panels[1]!.api.close).not.toHaveBeenCalled()
      expect(sdk.write.mock.calls.map(([request]) => request.body.path)).toEqual(action === 'save' || action === 'failure' ? ['/a.txt'] : [])
      expect(store.pendingClose).toBeNull()
      expect(!!store.fileDirty['file:/a.txt']).toBe(action === 'cancel' || action === 'failure')
    } finally {
      unregister()
    }
  })
})
