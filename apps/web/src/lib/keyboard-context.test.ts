// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { appKeyboardCommands, createKeyboardCommandRegistry, type AppKeyboardCommand } from './keyboard-commands'
import { canDispatchKeyboardCommand } from './keyboard-context'
import { handleBrowserKeyboardShortcut } from './browser-keyboard-shortcuts'

afterEach(() => { document.body.replaceChildren() })

describe('keyboard command context', () => {
  it.each([appKeyboardCommands.saveActiveFile, appKeyboardCommands.closeCurrentWorkspaceTab, appKeyboardCommands.toggleSidebar])('blocks hidden workspace effects on settings (%s)', (command) => {
    expect(canDispatchKeyboardCommand(command, '/settings/keyboard')).toBe(false)
    expect(canDispatchKeyboardCommand(command, '/chat/bot-a')).toBe(true)
  })

  it('keeps opening settings available in both app sections', () => {
    expect(canDispatchKeyboardCommand(appKeyboardCommands.openSettings, '/')).toBe(true)
    expect(canDispatchKeyboardCommand(appKeyboardCommands.openSettings, '/settings/bots')).toBe(true)
    expect(canDispatchKeyboardCommand(appKeyboardCommands.openSettings, '/login')).toBe(false)
  })

  it('lets the focused modal own keys until it closes', () => {
    const modal = document.createElement('div')
    modal.setAttribute('role', 'dialog')
    modal.tabIndex = -1
    document.body.append(modal)
    modal.focus()
    expect(canDispatchKeyboardCommand(appKeyboardCommands.saveActiveFile, '/')).toBe(false)
    expect(canDispatchKeyboardCommand(appKeyboardCommands.openSettings, '/')).toBe(false)
    modal.setAttribute('data-state', 'closed')
    expect(canDispatchKeyboardCommand(appKeyboardCommands.saveActiveFile, '/')).toBe(true)
  })

  it('allows media commands only in the owning lightbox and blocks nested dialogs', () => {
    const lightbox = document.createElement('div')
    lightbox.setAttribute('role', 'dialog')
    lightbox.dataset.keyboardScope = 'mediaLightbox'
    document.body.append(lightbox)
    expect(canDispatchKeyboardCommand(appKeyboardCommands.mediaLightboxNext, '/')).toBe(true)
    expect(canDispatchKeyboardCommand(appKeyboardCommands.closeCurrentWorkspaceTab, '/')).toBe(false)
    const nested = document.createElement('div')
    nested.setAttribute('role', 'alertdialog')
    lightbox.append(nested)
    expect(canDispatchKeyboardCommand(appKeyboardCommands.mediaLightboxNext, '/')).toBe(false)
  })

  it('lets an inactive scoped handler hand its combo back to a workspace command', () => {
    const registry = createKeyboardCommandRegistry(command => canDispatchKeyboardCommand(command, '/'))
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
    const registry = createKeyboardCommandRegistry(command => canDispatchKeyboardCommand(command, path))
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
