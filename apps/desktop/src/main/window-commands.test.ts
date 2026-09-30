import { describe, expect, it, vi } from 'vitest'
import { DESKTOP_KEYBOARD_COMMAND_CHANNEL, appKeyboardCommands } from '../shared/keyboard-commands'
import { dispatchFocusedWindowCommand } from './window-commands'

function createWindow(options: { loading?: boolean } = {}) {
  return {
    close: vi.fn(),
    isDestroyed: vi.fn(() => false),
    webContents: {
      isLoading: vi.fn(() => options.loading ?? false),
      send: vi.fn(),
    },
  }
}

describe('dispatchFocusedWindowCommand', () => {
  it('dispatches keyboard commands to the chat renderer', () => {
    const chatWindow = createWindow()

    const handled = dispatchFocusedWindowCommand(
      chatWindow,
      chatWindow,
      appKeyboardCommands.closeCurrentWorkspaceTab,
    )

    expect(handled).toBe(true)
    expect(chatWindow.webContents.send).toHaveBeenCalledWith(
      DESKTOP_KEYBOARD_COMMAND_CHANNEL,
      appKeyboardCommands.closeCurrentWorkspaceTab,
    )
    expect(chatWindow.close).not.toHaveBeenCalled()
  })

  it('keeps the close-tab command mapped to native close for non-chat windows', () => {
    const chatWindow = createWindow()
    const auxiliaryWindow = createWindow()

    const handled = dispatchFocusedWindowCommand(
      chatWindow,
      auxiliaryWindow,
      appKeyboardCommands.closeCurrentWorkspaceTab,
    )

    expect(handled).toBe(true)
    expect(auxiliaryWindow.close).toHaveBeenCalledOnce()
    expect(auxiliaryWindow.webContents.send).not.toHaveBeenCalled()
  })

  it('falls back to native close when the chat renderer cannot receive commands yet', () => {
    const chatWindow = createWindow({ loading: true })

    const handled = dispatchFocusedWindowCommand(
      chatWindow,
      chatWindow,
      appKeyboardCommands.closeCurrentWorkspaceTab,
    )

    expect(handled).toBe(true)
    expect(chatWindow.close).toHaveBeenCalledOnce()
    expect(chatWindow.webContents.send).not.toHaveBeenCalled()
  })
})
