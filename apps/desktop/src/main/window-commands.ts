import {
  DESKTOP_KEYBOARD_COMMAND_CHANNEL,
  appKeyboardCommands,
  type AppKeyboardCommand,
} from '../shared/keyboard-commands'

interface CommandWindow {
  close(): void
  isDestroyed(): boolean
  webContents: {
    isLoading?(): boolean
    send(channel: string, command: AppKeyboardCommand): void
  }
}

function canSendRendererCommand(window: CommandWindow): boolean {
  return !window.webContents.isLoading?.()
}

export function dispatchFocusedWindowCommand(
  chatWindow: CommandWindow | null,
  focusedWindow: CommandWindow | null,
  command: AppKeyboardCommand,
): boolean {
  if (!focusedWindow || focusedWindow.isDestroyed()) return false

  if (chatWindow && !chatWindow.isDestroyed() && focusedWindow === chatWindow) {
    if (canSendRendererCommand(focusedWindow)) {
      focusedWindow.webContents.send(DESKTOP_KEYBOARD_COMMAND_CHANNEL, command)
      return true
    }
  }

  if (command === appKeyboardCommands.closeCurrentWorkspaceTab) {
    focusedWindow.close()
    return true
  }

  return false
}
