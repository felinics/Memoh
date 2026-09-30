import { appKeyboardCommands, type AppKeyboardCommand } from './keyboard-commands'
import { keyboardBindings } from './keyboard-bindings'

export function canDispatchKeyboardCommand(command: AppKeyboardCommand, path: string, root: Document = document): boolean {
  const chatRoute = path === '/' || path === '/chat' || path.startsWith('/chat/')
  if (!chatRoute && !path.startsWith('/settings')) return false
  const dialogs = [...root.querySelectorAll<HTMLElement>('[role="dialog"], [role="alertdialog"]')]
    .filter(element => element.dataset.state !== 'closed' && !element.hidden
      && !element.closest('[aria-hidden="true"]')
      && root.defaultView?.getComputedStyle(element).display !== 'none'
      && root.defaultView?.getComputedStyle(element).visibility !== 'hidden')
  const dialog = dialogs.at(-1)
  const mediaCommand = keyboardBindings.find(binding => binding.command === command)?.scope === 'mediaLightbox'
  if (dialog) return mediaCommand && dialog.dataset.keyboardScope === 'mediaLightbox'
  if (mediaCommand) return false
  return command === appKeyboardCommands.openSettings || chatRoute
}
