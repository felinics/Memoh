import { appKeyboardCommands, type AppKeyboardCommand } from './keyboard-commands'
import { keyboardBindings } from './keyboard-bindings'

export function isKeyboardElementVisible(element: HTMLElement, root: Document = document): boolean {
  if (element.closest('[aria-hidden="true"]')) return false
  if (root.defaultView?.getComputedStyle(element).visibility === 'hidden') return false
  for (let ancestor: HTMLElement | null = element; ancestor; ancestor = ancestor.parentElement) {
    if (ancestor.hidden || root.defaultView?.getComputedStyle(ancestor).display === 'none') return false
  }
  return true
}

// Controls that take typed text (or keys) of their own: focus resting in one of
// them belongs to that control, not to the chat composer.
export const TEXT_INPUT_OWNER = 'input, textarea, select, [contenteditable]:not([contenteditable="false"]), [role="textbox"], [role="combobox"], [role="listbox"], [role="menu"], [role="tree"], [role="grid"], [role="slider"], [role="spinbutton"], .monaco-editor, .cm-editor, .xterm'

export function focusIsInTextInput(root: Document = document): boolean {
  return !!root.activeElement?.closest(TEXT_INPUT_OWNER)
}

export function activeKeyboardDialog(root: Document = document): HTMLElement | undefined {
  return [...root.querySelectorAll<HTMLElement>('[role="dialog"], [role="alertdialog"]')].filter(element => {
    return element.dataset.state !== 'closed' && isKeyboardElementVisible(element, root)
  }).at(-1)
}

export function selectActiveKeyboardBindings<T extends { scope: string }>(bindings: T[], root: Document = document): T[] {
  const mediaActive = activeKeyboardDialog(root)?.dataset.keyboardScope === 'mediaLightbox'
  if (!mediaActive) return bindings.filter(binding => binding.scope !== 'mediaLightbox')
  return [...bindings.filter(binding => binding.scope === 'mediaLightbox'), ...bindings.filter(binding => binding.scope !== 'mediaLightbox')]
}

export function canDispatchKeyboardCommand(command: AppKeyboardCommand, route: { name?: unknown; path: string }, root: Document = document, hasWorkspace = true): boolean {
  const chatRoute = route.name === 'home' || route.name === 'bot'
  const settingsRoute = route.path.startsWith('/settings')
  const closesWindow = command === appKeyboardCommands.closeCurrentWorkspaceTab && !hasWorkspace
  if (!chatRoute && !settingsRoute && !closesWindow) return false
  const dialog = activeKeyboardDialog(root)
  const mediaCommand = keyboardBindings.find(binding => binding.command === command)?.scope === 'mediaLightbox'
  if (dialog) return mediaCommand && dialog.dataset.keyboardScope === 'mediaLightbox'
  if (closesWindow || mediaCommand) return true
  return chatRoute || command === appKeyboardCommands.openSettings
}
