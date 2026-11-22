// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref, type App } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import ViewFile from '../components/workbench/view-file.vue'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { useChatSelectionStore } from '@/store/chat-selection'
import { KEYBOARD_REGISTRY } from '@/composables/useKeyboardCommand'
import { appKeyboardCommands, createKeyboardCommandRegistry } from '@/lib/keyboard-commands'
import { connectBrowserKeyboardShortcutsLive } from '@/lib/browser-keyboard-shortcuts'
import { keyboardBindings, selectWebBindings } from '@/lib/keyboard-bindings'
import { registerWorkspaceTabCommands } from './workspace-tab-commands'
import { registerWorkbenchCommands } from './workbench-commands'
import { useComposerKeyboardFocus } from '../composables/useComposerKeyboardFocus'

const sdk = vi.hoisted(() => {
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
  return { read: vi.fn(), write: vi.fn() }
})
vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdContainerFsRead: sdk.read,
  postBotsByBotIdContainerFsWrite: sdk.write,
  getBotsByBotIdContainerFs: vi.fn(),
  getBotsByBotIdContainerFsDownload: vi.fn(),
}))
vi.mock('@felinic/ui', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
  Button: 'button',
  DiffTitleBar: 'div',
  PanePlaceholder: 'div',
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/i18n', () => ({ default: { global: { t: (key: string) => key, locale: ref('en') } } }))
vi.mock('@/lib/api-client', () => ({ sdkApiUrl: vi.fn(), sdkAuthQuery: vi.fn() }))
vi.mock('@/composables/useEnvironment', () => ({
  useCompactShell: () => ref(false),
  useCoarsePointer: () => ref(false),
}))
vi.mock('@/store/chat-list', async () => {
  const { defineStore } = await import('pinia')
  return {
    useChatStore: defineStore('keyboard-chat-fixture', () => ({
      fsChangedAt: ref(0),
      currentBotId: ref('bot-a'),
      bots: ref([{ id: 'bot-a', current_user_permissions: ['workspace_read', 'workspace_write'] }]),
      affectsPath: () => false,
      fsEventForPath: () => null,
      focusChatView: vi.fn(),
      selectDraft: vi.fn(),
      selectSession: vi.fn(),
      resetToEmptyComposer: vi.fn(),
      knownSessionSummary: () => null,
      isSessionTentative: () => false,
      sessions: ref([]),
      knownSessions: ref([]),
      sessionId: ref(null),
      loadingChats: ref(false),
      hasExplicitSessionSelection: ref(false),
      activeSession: ref(null),
      pendingExternalAgentSessionInput: ref(null),
      draftPromoted: ref(null),
      userSentInSession: ref(null),
      draftViewRequested: ref(null),
      forkedSessionRequested: ref(null),
      deletedSession: ref(null),
    })),
  }
})
vi.mock('../components/workbench/workbench-breadcrumb.vue', () => ({ default: 'div' }))
vi.mock('@/components/monaco-editor/diff.vue', () => ({ default: 'div' }))
vi.mock('@/components/monaco-editor/index.vue', () => ({
  default: (props: { modelValue: string, 'onUpdate:modelValue': (value: string) => void }) => h('textarea', {
    value: props.modelValue,
    onInput: (event: Event) => props['onUpdate:modelValue']((event.target as HTMLTextAreaElement).value),
  }),
}))

let app: App | undefined
let host: HTMLDivElement | undefined
let pinia: ReturnType<typeof createPinia>
let disconnect = () => {}
let store: ReturnType<typeof useWorkspaceTabsStore>

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  pinia = createPinia()
  setActivePinia(pinia)
  sdk.read.mockResolvedValue({ data: { content: 'initial', revision: 'r0' } })
  sdk.write.mockResolvedValue({ data: { revision: 'r1' } })
  sdk.write.mockClear()
  useChatSelectionStore().setBot('bot-a')
  store = useWorkspaceTabsStore()
})

afterEach(() => {
  disconnect()
  app?.unmount()
  host?.remove()
  store.unmountWorkspace()
  store.$dispose()
})

async function mountSplitFiles() {
  store.mountWorkspace()
  store.openFilePinned('/a.txt')
  store.openFileToSide('/b.txt')
  store.activateTab('file:/a.txt')

  const registry = createKeyboardCommandRegistry()
  host = document.createElement('div')
  document.body.append(host)
  app = createApp({
    setup: () => () => h('div', [
      h(ViewFile, { tabId: 'file:/a.txt', filePath: '/a.txt', visible: Boolean(store.workbench.tabs['file:/a.txt']) }),
      h(ViewFile, { tabId: 'file:/b.txt', filePath: '/b.txt', visible: Boolean(store.workbench.tabs['file:/b.txt']) }),
    ]),
  }).use(pinia).provide(KEYBOARD_REGISTRY, registry)
  app.mount(host)
  disconnect = connectBrowserKeyboardShortcutsLive(registry, () => selectWebBindings(keyboardBindings))
  await vi.waitFor(() => expect(host!.querySelectorAll('textarea')).toHaveLength(2))
  const editors = [...host.querySelectorAll('textarea')]
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
  const activate = (index: number) => store.activateTab(index === 0 ? 'file:/a.txt' : 'file:/b.txt')
  return { edit, save, activate }
}

describe('workspace keyboard ownership', () => {
  it('transfers a focus shortcut through the store to a late, initially hidden composer', async () => {
    store.mountWorkspace()
    store.openDraftChat()
    const chatId = store.activeId!
    store.openFilePinned('/a.txt')
    expect(store.activeId).toBe('file:/a.txt')

    const registry = createKeyboardCommandRegistry()
    const unregister = registerWorkbenchCommands(registry, store)
    const removeListener = connectBrowserKeyboardShortcutsLive(registry, () => selectWebBindings(keyboardBindings))
    disconnect = () => { unregister(); removeListener() }

    const key = new KeyboardEvent('keydown', { key: 'Enter', altKey: true, shiftKey: true, bubbles: true, cancelable: true })
    document.body.dispatchEvent(key)
    expect(key.defaultPrevented).toBe(true)
    expect(store.pendingChatInputFocus).toEqual({ panelId: chatId, botId: 'bot-a', sessionId: null })

    const ready = ref(false)
    host = document.createElement('div')
    host.style.visibility = 'hidden'
    document.body.append(host)
    app = createApp({
      setup() {
        const textarea = ref<HTMLTextAreaElement | null>(null)
        useComposerKeyboardFocus({
          textarea,
          enabled: () => store.activeId === chatId,
          available: () => true,
          ready: () => ready.value,
          owner: () => `bot-a:${chatId}:`,
          request: () => store.pendingChatInputFocus?.panelId === chatId,
          consumeRequest: () => { store.pendingChatInputFocus = null },
        })
        return () => h('textarea', { ref: textarea, disabled: !ready.value })
      },
    }).use(pinia)
    app.mount(host)
    ready.value = true
    await nextTick()
    expect(document.activeElement).toBe(document.body)
    host.style.visibility = 'visible'
    await vi.waitFor(() => expect(document.activeElement).toBe(host!.querySelector('textarea')))
    expect(store.pendingChatInputFocus).toBeNull()
  })

  it('saves only the focused split and follows a later focus change', async () => {
    const view = await mountSplitFiles()
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
    const view = await mountSplitFiles()
    await view.edit(1, 'B')
    await view.save(0)
    expect(sdk.write).not.toHaveBeenCalled()
    expect(store.fileDirty['file:/b.txt']).toBe(true)
  })

  it('consumes a second save press while the first write is pending', async () => {
    const view = await mountSplitFiles()
    await view.edit(0, 'A')
    let finish!: (value: { data: { revision: string } }) => void
    sdk.write.mockReturnValueOnce(new Promise((resolve) => { finish = resolve }))
    expect((await view.save(0)).defaultPrevented).toBe(true)
    expect((await view.save(0)).defaultPrevented).toBe(true)
    expect(sdk.write).toHaveBeenCalledOnce()
    finish({ data: { revision: 'r1' } })
    await vi.waitFor(() => expect(store.dirtyFileCount).toBe(0))
  })

  it.each(['cancel', 'discard', 'save', 'failure'] as const)('protects dirty close through %s after focus moves', async (action) => {
    const view = await mountSplitFiles()
    await view.edit(0, 'A')
    const registry = createKeyboardCommandRegistry()
    const unregister = registerWorkspaceTabCommands(registry, store)
    try {
      registry.dispatch(appKeyboardCommands.closeCurrentWorkspaceTab)
      registry.dispatch(appKeyboardCommands.closeCurrentWorkspaceTab)
      expect(store.workbench.tabs['file:/a.txt']).toBeDefined()
      expect(store.pendingClose?.panelId).toBe('file:/a.txt')
      view.activate(1)
      await nextTick()
      if (action === 'failure') sdk.write.mockRejectedValueOnce(new Error('write failed'))
      await store.resolvePendingClose(action === 'failure' ? 'save' : action)

      expect(Boolean(store.workbench.tabs['file:/a.txt'])).toBe(action === 'cancel' || action === 'failure')
      expect(store.workbench.tabs['file:/b.txt']).toBeDefined()
      expect(sdk.write.mock.calls.map(([request]) => request.body.path)).toEqual(
        action === 'save' || action === 'failure' ? ['/a.txt'] : [],
      )
      expect(store.pendingClose).toBeNull()
      expect(Boolean(store.fileDirty['file:/a.txt'])).toBe(action === 'cancel' || action === 'failure')
    } finally {
      unregister()
    }
  })
})
