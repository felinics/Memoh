// @vitest-environment jsdom
import { createApp, h, KeepAlive, nextTick, ref, type App } from 'vue'
import { createPinia } from 'pinia'
import { afterEach, expect, it, vi } from 'vitest'
import KeyCaptureDialog from './KeyCaptureDialog.vue'
import i18n from '@/i18n'
import { DesktopWindowKey } from '@/lib/desktop-shell'
import { appKeyboardCommands, createKeyboardCommandRegistry } from '@/lib/keyboard-commands'
import { connectBrowserKeyboardShortcutsLive } from '@/lib/browser-keyboard-shortcuts'
import { keyboardBindings } from '@/lib/keyboard-bindings'

const toastError = vi.hoisted(() => vi.fn())
vi.mock('@felinic/ui', async importOriginal => ({
  ...await importOriginal<typeof import('@felinic/ui')>(),
  toast: { error: toastError, success: vi.fn() },
}))

let app: App | undefined
let host: HTMLElement | undefined
let disconnect = () => {}
afterEach(() => { disconnect(); app?.unmount(); host?.remove() })

it('suppresses native menus during capture and restores them on close and disposal', async () => {
  const setIgnoreMenuShortcuts = vi.fn(async (_ignored: boolean) => {})
  const open = ref(true)
  const registry = createKeyboardCommandRegistry()
  const closeTab = vi.fn(() => true)
  registry.register(appKeyboardCommands.closeCurrentWorkspaceTab, closeTab)
  disconnect = connectBrowserKeyboardShortcutsLive(registry, () => keyboardBindings)
  host = document.createElement('div')
  document.body.append(host)
  app = createApp({ setup: () => () => h(KeyCaptureDialog, {
    open: open.value,
    command: appKeyboardCommands.toggleSidebar,
    i18nKey: 'toggleSidebar',
    'onUpdate:open': (value: boolean) => { open.value = value },
  }) }).use(createPinia()).use(i18n).provide(DesktopWindowKey, {
    isFullScreen: async () => false,
    onFullScreenChanged: () => () => {},
    setIgnoreMenuShortcuts,
  })
  app.mount(host)
  await nextTick()
  expect(setIgnoreMenuShortcuts).toHaveBeenLastCalledWith(true)
  document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'w', ctrlKey: true, bubbles: true, cancelable: true }))
  expect(closeTab).not.toHaveBeenCalled()
  open.value = false
  await nextTick()
  expect(setIgnoreMenuShortcuts).toHaveBeenLastCalledWith(false)
  open.value = true
  await nextTick()
  expect(setIgnoreMenuShortcuts).toHaveBeenLastCalledWith(true)
  app.unmount()
  app = undefined
  expect(setIgnoreMenuShortcuts).toHaveBeenLastCalledWith(false)
})

it('releases capture when a cached settings page deactivates and returns with the dialog closed', async () => {
  const setIgnoreMenuShortcuts = vi.fn(async (_ignored: boolean) => {})
  const open = ref(true)
  const visible = ref(true)
  const page = { setup: () => () => h(KeyCaptureDialog, {
    open: open.value, command: appKeyboardCommands.toggleSidebar, i18nKey: 'toggleSidebar',
    'onUpdate:open': (value: boolean) => { open.value = value },
  }) }
  host = document.createElement('div')
  document.body.append(host)
  app = createApp({ setup: () => () => h(KeepAlive, null, { default: () => visible.value ? h(page) : null }) })
    .use(createPinia()).use(i18n).provide(DesktopWindowKey, {
      isFullScreen: async () => false, onFullScreenChanged: () => () => {}, setIgnoreMenuShortcuts,
    })
  app.mount(host)
  await nextTick()
  expect(setIgnoreMenuShortcuts).toHaveBeenLastCalledWith(true)
  visible.value = false
  await nextTick()
  expect(open.value).toBe(false)
  expect(setIgnoreMenuShortcuts).toHaveBeenLastCalledWith(false)
  const event = new KeyboardEvent('keydown', { key: 's', ctrlKey: true, bubbles: true, cancelable: true })
  document.body.dispatchEvent(event)
  expect(event.defaultPrevented).toBe(false)
  visible.value = true
  await nextTick()
  expect(setIgnoreMenuShortcuts).toHaveBeenLastCalledWith(false)
})

it('explains and blocks a text editing combo before accepting a free one', async () => {
  host = document.createElement('div')
  document.body.append(host)
  app = createApp({ setup: () => () => h(KeyCaptureDialog, {
    open: true, command: appKeyboardCommands.newTerminal, i18nKey: 'newTerminal',
  }) }).use(createPinia()).use(i18n)
  app.mount(host)
  await nextTick()
  const save = () => [...document.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Save')!
  window.dispatchEvent(new KeyboardEvent('keydown', { key: 'c', ctrlKey: true, bubbles: true, cancelable: true }))
  await nextTick()
  expect(document.body.textContent).toContain(i18n.global.t('settings.keyboard.dialog.editingError'))
  expect(save().disabled).toBe(true)
  window.dispatchEvent(new KeyboardEvent('keydown', { key: 'z', ctrlKey: true, altKey: true, bubbles: true, cancelable: true }))
  await nextTick()
  expect(document.body.textContent).not.toContain(i18n.global.t('settings.keyboard.dialog.editingError'))
  expect(save().disabled).toBe(false)
})

it('reports a failed menu shortcut pause and a failed restore with their own messages', async () => {
  toastError.mockClear()
  const setIgnoreMenuShortcuts = vi.fn(async (_ignored: boolean) => { throw new Error('ipc') })
  const open = ref(true)
  host = document.createElement('div')
  document.body.append(host)
  app = createApp({ setup: () => () => h(KeyCaptureDialog, {
    open: open.value, command: appKeyboardCommands.toggleSidebar, i18nKey: 'toggleSidebar',
  }) }).use(createPinia()).use(i18n).provide(DesktopWindowKey, {
    isFullScreen: async () => false, onFullScreenChanged: () => () => {}, setIgnoreMenuShortcuts,
  })
  app.mount(host)
  await vi.waitFor(() => expect(toastError).toHaveBeenLastCalledWith(i18n.global.t('settings.keyboard.dialog.menuPauseFailed')))
  open.value = false
  await vi.waitFor(() => expect(toastError).toHaveBeenLastCalledWith(i18n.global.t('settings.keyboard.dialog.menuRestoreFailed')))
  expect(i18n.global.t('settings.keyboard.dialog.menuPauseFailed')).not.toBe('settings.keyboard.dialog.menuPauseFailed')
})
