import { appKeyboardCommands, type AppKeyboardCommand, type KeyboardCommandRegistry } from '@/lib/keyboard-commands'
import type { useWorkspaceTabsStore } from '@/store/workspace-tabs'

export function registerWorkbenchCommands(registry: KeyboardCommandRegistry, store: ReturnType<typeof useWorkspaceTabsStore>): () => void {
  const sidebar = (view: 'sessions' | 'files' | 'schedule' | 'supermarket') => {
    if (view === 'supermarket' && store.isMobile) return
    store.selectSidebarView(view)
    if (store.isMobile) store.openMobileNav()
  }
  const split = (direction: 'right' | 'below') => {
    const groupId = store.api?.activeGroup?.id
    if (groupId) store.splitGroup(groupId, direction)
  }
  const actions: Array<[AppKeyboardCommand, () => void]> = [
    [appKeyboardCommands.newChatSession, () => store.openDraftChat({ groupId: store.api?.activeGroup?.id })],
    [appKeyboardCommands.showSessions, () => sidebar('sessions')],
    [appKeyboardCommands.showFiles, () => sidebar('files')],
    [appKeyboardCommands.showSchedule, () => sidebar('schedule')],
    [appKeyboardCommands.showSupermarket, () => sidebar('supermarket')],
    [appKeyboardCommands.nextWorkspaceTab, () => { store.focusAdjacentTab(1) }],
    [appKeyboardCommands.previousWorkspaceTab, () => { store.focusAdjacentTab(-1) }],
    [appKeyboardCommands.splitWorkspaceRight, () => split('right')],
    [appKeyboardCommands.splitWorkspaceBelow, () => split('below')],
    [appKeyboardCommands.newTerminal, () => store.openTerminal()],
    [appKeyboardCommands.newBrowser, () => store.openBrowser()],
  ]
  const unregister = actions.map(([command, action]) => registry.register(command, () => {
    action()
    return true
  }))
  unregister.push(registry.register(appKeyboardCommands.focusChatInput, () => {
    store.requestChatInputFocus()
    return true
  }))
  return () => { unregister.forEach(dispose => dispose()) }
}
