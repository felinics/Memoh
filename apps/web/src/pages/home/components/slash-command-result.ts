import type { CommandActionListItem, CommandActionResult } from '@/composables/api/useChat'

export type CommandResultSelection
  = | { kind: 'quick_action', id: string, text: string }
    | { kind: 'acp_permission', modeId: string }
    | { kind: 'skill', id: string, title: string, description?: string }

export interface PermissionCommandResultCopy {
  modesTitle: string
  modesText: string
  changedTitle: string
  changedText: string
  currentMode: string
}

const QUICK_ACTION_SLASH_TEXT: Record<string, string> = {
  help: '/help',
  'skill.list': '/skill list',
}

function commandResultItemKind(item: CommandActionListItem): string {
  return item.kind?.trim().toLowerCase() ?? ''
}

function commandResultQuickActionID(item: CommandActionListItem): string {
  return item.id?.trim() ?? ''
}

function isCurrentQuickAction(item: CommandActionListItem, currentActionID = ''): boolean {
  const id = commandResultQuickActionID(item)
  return !!id && id === currentActionID.trim()
}

export function resolveCommandResultSelection(
  item: CommandActionListItem,
  currentActionID = '',
): CommandResultSelection | null {
  const kind = commandResultItemKind(item)
  if (kind === 'acp_mode') {
    const modeId = item.id ?? ''
    return modeId
      ? { kind: 'acp_permission', modeId }
      : null
  }
  const id = commandResultQuickActionID(item)
  if (kind === 'skill') {
    const title = item.title.trim()
    return id && title
      ? { kind: 'skill', id, title: item.title, description: item.description }
      : null
  }
  if (kind !== 'quick_action' || isCurrentQuickAction(item, currentActionID)) return null

  const knownText = id ? QUICK_ACTION_SLASH_TEXT[id] : ''
  if (knownText) return { kind: 'quick_action', id, text: knownText }

  const title = item.title.trim()
  return title.startsWith('/')
    ? { kind: 'quick_action', id: id || title, text: title }
    : null
}

export function isCommandResultItemSelectable(item: CommandActionListItem, currentActionID = ''): boolean {
  return resolveCommandResultSelection(item, currentActionID) !== null
}

// The current ACP mode is reported as its own non-selectable row
// (`acp_mode_current`): the panel must show it so `/permission` answers "which
// mode am I in", but selecting it would be a no-op mode switch.
export function isCommandResultItemDisplayOnly(item: CommandActionListItem): boolean {
  return commandResultItemKind(item) === 'acp_mode_current'
}

export function isCommandResultItemVisible(item: CommandActionListItem, currentActionID = ''): boolean {
  return isCommandResultItemDisplayOnly(item) || isCommandResultItemSelectable(item, currentActionID)
}

export function commandResultPresentation(
  result: CommandActionResult,
  copy: PermissionCommandResultCopy,
): CommandActionResult {
  if (result.kind !== 'permission_modes' && result.kind !== 'permission_mode_changed') return result
  const changed = result.kind === 'permission_mode_changed'
  return {
    ...result,
    title: changed ? copy.changedTitle : copy.modesTitle,
    text: changed ? copy.changedText : copy.modesText,
    items: (result.items ?? []).map(item => {
      if (item.kind !== 'acp_mode_current') return item
      const description = item.description?.trim() ?? ''
      return {
        ...item,
        description: description
          ? `${copy.currentMode} · ${description}`
          : copy.currentMode,
      }
    }),
  }
}
