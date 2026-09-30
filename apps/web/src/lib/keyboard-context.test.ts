// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { appKeyboardCommands, createKeyboardCommandRegistry, type AppKeyboardCommand } from './keyboard-commands'
import { canDispatchKeyboardCommand } from './keyboard-context'
import { handleBrowserKeyboardShortcut } from './browser-keyboard-shortcuts'
import { createMemoryHistory, createRouter } from 'vue-router'
import { createAppRoutes } from '@/routes'

const router = createRouter({ history: createMemoryHistory(), routes: createAppRoutes('web') })
function route(path: string) { return router.resolve(path) }

afterEach(() => { document.body.replaceChildren() })

describe('keyboard command context', () => {
  it.each([
    ['web', '/bot/keyboard-qa'],
    ['desktop', '/bot/keyboard-qa/session-a'],
  ] as const)('accepts the actual %s Bot route', (platform, path) => {
    const router = createRouter({ history: createMemoryHistory(), routes: createAppRoutes(platform) })
    const route = router.resolve(path)
    expect(route.name).toBe('bot')
    expect(canDispatchKeyboardCommand(appKeyboardCommands.saveActiveFile, route)).toBe(true)
    expect(canDispatchKeyboardCommand(appKeyboardCommands.openSettings, route)).toBe(true)
  })
  it.each([appKeyboardCommands.saveActiveFile, appKeyboardCommands.closeCurrentWorkspaceTab, appKeyboardCommands.toggleSidebar])('blocks hidden workspace effects on settings (%s)', (command) => {
    expect(canDispatchKeyboardCommand(command, route('/settings/keyboard'))).toBe(false)
    expect(canDispatchKeyboardCommand(command, route('/bot/bot-a'))).toBe(true)
  })

  it('keeps opening settings available in both app sections', () => {
    expect(canDispatchKeyboardCommand(appKeyboardCommands.openSettings, route('/'))).toBe(true)
    expect(canDispatchKeyboardCommand(appKeyboardCommands.openSettings, route('/settings/bots'))).toBe(true)
    expect(canDispatchKeyboardCommand(appKeyboardCommands.openSettings, route('/login'))).toBe(false)
  })

  it('permits window-close fallback only without a workspace target, never through a modal', () => {
    expect(canDispatchKeyboardCommand(appKeyboardCommands.closeCurrentWorkspaceTab, route('/login'), document, false)).toBe(true)
    expect(canDispatchKeyboardCommand(appKeyboardCommands.closeCurrentWorkspaceTab, route('/login'), document, true)).toBe(false)
    const dialog = document.createElement('div')
    dialog.setAttribute('role', 'dialog')
    document.body.append(dialog)
    expect(canDispatchKeyboardCommand(appKeyboardCommands.closeCurrentWorkspaceTab, route('/login'), document, false)).toBe(false)
  })

  it('lets the focused modal own keys until it closes', () => {
    const modal = document.createElement('div')
    modal.setAttribute('role', 'dialog')
    modal.tabIndex = -1
    document.body.append(modal)
    modal.focus()
    expect(canDispatchKeyboardCommand(appKeyboardCommands.saveActiveFile, route('/'))).toBe(false)
    expect(canDispatchKeyboardCommand(appKeyboardCommands.openSettings, route('/'))).toBe(false)
    modal.setAttribute('data-state', 'closed')
    expect(canDispatchKeyboardCommand(appKeyboardCommands.saveActiveFile, route('/'))).toBe(true)
  })

  it('allows media commands only in the owning lightbox and blocks nested dialogs', () => {
    const lightbox = document.createElement('div')
    lightbox.setAttribute('role', 'dialog')
    lightbox.dataset.keyboardScope = 'mediaLightbox'
    document.body.append(lightbox)
    expect(canDispatchKeyboardCommand(appKeyboardCommands.mediaLightboxNext, route('/'))).toBe(true)
    expect(canDispatchKeyboardCommand(appKeyboardCommands.closeCurrentWorkspaceTab, route('/'))).toBe(false)
    const nested = document.createElement('div')
    nested.setAttribute('role', 'alertdialog')
    lightbox.append(nested)
    expect(canDispatchKeyboardCommand(appKeyboardCommands.mediaLightboxNext, route('/'))).toBe(false)
  })

  it('lets an inactive scoped handler hand its combo back to a workspace command', () => {
    const registry = createKeyboardCommandRegistry(command => canDispatchKeyboardCommand(command, route('/')))
    const save = vi.fn(() => true)
    registry.register(appKeyboardCommands.closeMediaLightbox, () => false)
    registry.register(appKeyboardCommands.saveActiveFile, save)
    handleBrowserKeyboardShortcut(new KeyboardEvent('keydown', { key: 's', ctrlKey: true }), registry, [
      { command: appKeyboardCommands.closeMediaLightbox, key: 's', mod: true },
      { command: appKeyboardCommands.saveActiveFile, key: 's', mod: true },
    ], 'linux')
    expect(save).toHaveBeenCalledOnce()
  })

  it('consumes blocked menu delivery without closing the window and resumes after dismissal', () => {
    let path = '/settings/keyboard'
    const registry = createKeyboardCommandRegistry(command => canDispatchKeyboardCommand(command, route(path)))
    const closeTab = vi.fn(() => true)
    const closeWindow = vi.fn()
    let menu: (command: AppKeyboardCommand) => void = () => {}
    registry.register(appKeyboardCommands.closeCurrentWorkspaceTab, closeTab)
    registry.connect({ onKeyboardCommand: cb => { menu = cb } }, closeWindow)
    menu(appKeyboardCommands.closeCurrentWorkspaceTab)
    expect(closeTab).not.toHaveBeenCalled()
    expect(closeWindow).not.toHaveBeenCalled()
    path = '/'
    menu(appKeyboardCommands.closeCurrentWorkspaceTab)
    expect(closeTab).toHaveBeenCalledOnce()
    expect(closeWindow).not.toHaveBeenCalled()
  })
})
