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
