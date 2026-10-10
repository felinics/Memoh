import {
  appKeyboardCommands,
  type AppKeyboardCommand,
  type KeyboardCommandRegistry,
} from '@/lib/keyboard-commands'

export interface WorkspaceTabCommandStore {
  activeId: string | null
  requestCloseTab(id: string): void
}

export function handleWorkspaceKeyboardCommand(
  command: AppKeyboardCommand,
  store: WorkspaceTabCommandStore,
): boolean {
  if (command !== appKeyboardCommands.closeCurrentWorkspaceTab) return false
  const activeId = store.activeId
  if (!activeId) return false
  store.requestCloseTab(activeId)
  return true
}

export function registerWorkspaceTabCommands(
  registry: Pick<KeyboardCommandRegistry, 'register'>,
  store: WorkspaceTabCommandStore,
): () => void {
  return registry.register(appKeyboardCommands.closeCurrentWorkspaceTab, () =>
    handleWorkspaceKeyboardCommand(appKeyboardCommands.closeCurrentWorkspaceTab, store),
  )
}
