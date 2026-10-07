// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { connectBrowserKeyboardShortcutsLive } from '@/lib/browser-keyboard-shortcuts'
import { appKeyboardCommands, createKeyboardCommandRegistry, type AppKeyboardCommand } from '@/lib/keyboard-commands'
import { selectActiveKeyboardBindings } from '@/lib/keyboard-context'
import { useKeyboardShortcutsStore } from '@/store/keyboard-shortcuts'

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('@felinic/ui', () => ({ Button: 'button' }))
vi.mock('@/lib/api-client', () => ({ sdkAuthQuery: () => ({}), sdkWebSocketUrl: () => 'ws://terminal' }))
vi.mock('@/store/settings', () => ({ useSettingsStore: () => ({ codeFontSizePx: 13, codeFontFamily: '', codeFontStack: '' }) }))
vi.mock('@/store/workspace-tabs', () => ({ useWorkspaceTabsStore: () => ({ updateTerminalTitle: vi.fn(), closeTab: vi.fn(), openBrowserAt: vi.fn() }) }))

const sent: string[] = []
class FakeSocket {
  static OPEN = 1
  readyState = 1
  binaryType = ''
  onopen: (() => void) | null = null
  onmessage = null
  onclose = null
  onerror = null
  send(data: Uint8Array | string) { sent.push(typeof data === 'string' ? data : new TextDecoder().decode(data)) }
  close() {}
}

let app: App | undefined
let disconnect = () => {}
beforeEach(() => {
  localStorage.clear()
  sent.length = 0
  setActivePinia(createPinia())
  vi.stubGlobal('WebSocket', FakeSocket)
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  window.matchMedia = (() => ({ matches: false, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {} })) as never
})
afterEach(() => {
  disconnect()
  app?.unmount()
  document.body.replaceChildren()
  vi.unstubAllGlobals()
})

async function mountTerminal() {
  const registry = createKeyboardCommandRegistry()
  const ran: AppKeyboardCommand[] = []
  for (const command of Object.values(appKeyboardCommands)) registry.register(command, () => { ran.push(command); return true })
  const shortcuts = useKeyboardShortcutsStore()
  disconnect = connectBrowserKeyboardShortcutsLive(registry, () => selectActiveKeyboardBindings(shortcuts.effectiveBindings))
  const TerminalPane = (await import('./terminal-pane.vue')).default
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(TerminalPane, { botId: 'bot', tabId: 'terminal:1', active: true })
  app.mount(root)
  await nextTick()
  await nextTick()
  const textarea = root.querySelector('textarea')!
  const press = (init: KeyboardEventInit & { keyCode?: number }) => {
    textarea.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }))
  }
  return { ran, press, shortcuts }
}

describe('terminal keyboard ownership', () => {
  it.each([
    [appKeyboardCommands.nextWorkspaceTab, { key: 'PageDown', keyCode: 34 }],
    [appKeyboardCommands.previousWorkspaceTab, { key: 'PageUp', keyCode: 33 }],
    [appKeyboardCommands.focusChatInput, { key: 'Enter', keyCode: 13 }],
    [appKeyboardCommands.showSessions, { key: '!', code: 'Digit1', keyCode: 49 }],
    [appKeyboardCommands.showSupermarket, { key: '$', code: 'Digit4', keyCode: 52 }],
  ])('hands %s to the workbench instead of the shell', async (command, key) => {
    const { ran, press } = await mountTerminal()
    press({ ...key, altKey: true, shiftKey: true })
    expect(ran).toEqual([command])
    expect(sent).toEqual([])
  })

  it('keeps the other workspace shortcuts for the shell', async () => {
    const { ran, press } = await mountTerminal()
    press({ key: 'X', code: 'KeyX', keyCode: 88, altKey: true, shiftKey: true })
    press({ key: 'w', code: 'KeyW', keyCode: 87, ctrlKey: true })
    expect(ran).toEqual([])
    expect(sent).toEqual(['\x1bX', '\x17'])
  })

  it('follows a rebound navigation shortcut', async () => {
    const { ran, press, shortcuts } = await mountTerminal()
    shortcuts.setBinding(appKeyboardCommands.showFiles, 'Mod+Shift+F9')
    press({ key: 'F9', keyCode: 120, ctrlKey: true, shiftKey: true })
    press({ key: '@', code: 'Digit2', keyCode: 50, altKey: true, shiftKey: true })
    expect(ran).toEqual([appKeyboardCommands.showFiles])
    expect(sent).toEqual(['\x1b@'])
  })

  it('keeps a key for the shell when the binding that owns it does not leave the terminal', async () => {
    localStorage.setItem('keyboard-shortcuts-overrides', JSON.stringify({ [appKeyboardCommands.newBrowser]: 'Alt+Shift+!' }))
    const { ran, press } = await mountTerminal()
    press({ key: '!', code: 'Digit1', keyCode: 49, altKey: true, shiftKey: true })
    expect(ran).toEqual([])
    expect(sent).toEqual(['\x1b!'])
  })

  it('ignores a closed lightbox that shares the navigation key', async () => {
    localStorage.setItem('keyboard-shortcuts-overrides', JSON.stringify({ [appKeyboardCommands.mediaLightboxPrev]: 'Alt+Shift+PageUp' }))
    const { ran, press } = await mountTerminal()
    press({ key: 'PageUp', keyCode: 33, altKey: true, shiftKey: true })
    expect(ran).toEqual([appKeyboardCommands.previousWorkspaceTab])
    expect(sent).toEqual([])
  })

  it('lets the workbench have a shortcut the terminal does not send to the shell', async () => {
    localStorage.setItem('keyboard-shortcuts-overrides', JSON.stringify({ [appKeyboardCommands.newBrowser]: 'Mod+Shift+1' }))
    const { ran, press } = await mountTerminal()
    press({ key: '!', code: 'Digit1', keyCode: 49, ctrlKey: true, shiftKey: true })
    expect(sent).toEqual([])
    expect(ran).toEqual([appKeyboardCommands.newBrowser])
  })
})
