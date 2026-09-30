import { appKeyboardCommands, type AppKeyboardCommand } from './keyboard-commands'
import { keyboardBindings } from './keyboard-bindings'

export function canDispatchKeyboardCommand(command: AppKeyboardCommand, route: { name?: unknown; path: string }, root: Document = document, hasWorkspace = true): boolean {
  const chatRoute = route.name === 'home' || route.name === 'bot'
  const dialogs = [...root.querySelectorAll<HTMLElement>('[role="dialog"], [role="alertdialog"]')]
    .filter(element => element.dataset.state !== 'closed' && !element.hidden
      && !element.closest('[aria-hidden="true"]')
      && root.defaultView?.getComputedStyle(element).display !== 'none'
      && root.defaultView?.getComputedStyle(element).visibility !== 'hidden')
  const dialog = dialogs.at(-1)
  const mediaCommand = keyboardBindings.find(binding => binding.command === command)?.scope === 'mediaLightbox'
  if (dialog) return mediaCommand && dialog.dataset.keyboardScope === 'mediaLightbox'
  if (command === appKeyboardCommands.closeCurrentWorkspaceTab && !hasWorkspace) return true
  if (!chatRoute && !route.path.startsWith('/settings')) return false
  if (mediaCommand) return true
  return command === appKeyboardCommands.openSettings || chatRoute
}
