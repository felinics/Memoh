import { appKeyboardCommands, type AppKeyboardCommand } from './keyboard-commands'
import { keyboardBindings } from './keyboard-bindings'

function activeKeyboardDialog(root: Document): HTMLElement | undefined {
  return [...root.querySelectorAll<HTMLElement>('[role="dialog"], [role="alertdialog"]')].filter(element => {
    if (element.dataset.state === 'closed' || element.closest('[aria-hidden="true"]')) return false
    if (root.defaultView?.getComputedStyle(element).visibility === 'hidden') return false
    for (let ancestor: HTMLElement | null = element; ancestor; ancestor = ancestor.parentElement) {
      if (ancestor.hidden || root.defaultView?.getComputedStyle(ancestor).display === 'none') return false
    }
    return true
  }).at(-1)
}

export function selectActiveKeyboardBindings<T extends { scope: string }>(bindings: T[], root: Document = document): T[] {
  const mediaActive = activeKeyboardDialog(root)?.dataset.keyboardScope === 'mediaLightbox'
  return bindings.filter(binding => binding.scope !== 'mediaLightbox' || mediaActive)
}

export function canDispatchKeyboardCommand(command: AppKeyboardCommand, route: { name?: unknown; path: string }, root: Document = document, hasWorkspace = true): boolean {
  const chatRoute = route.name === 'home' || route.name === 'bot'
  if (!chatRoute && !route.path.startsWith('/settings') && (command !== appKeyboardCommands.closeCurrentWorkspaceTab || hasWorkspace)) return false
  const dialog = activeKeyboardDialog(root)
  const mediaCommand = keyboardBindings.find(binding => binding.command === command)?.scope === 'mediaLightbox'
  if (dialog) return mediaCommand && dialog.dataset.keyboardScope === 'mediaLightbox'
  if (command === appKeyboardCommands.closeCurrentWorkspaceTab && !hasWorkspace) return true
  if (mediaCommand) return true
  return command === appKeyboardCommands.openSettings || chatRoute
}
