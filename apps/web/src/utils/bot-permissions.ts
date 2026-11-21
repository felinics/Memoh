export type BotPermission =
  | 'chat'
  | 'workspace_read'
  | 'workspace_write'
  | 'workspace_exec'
  | 'manage'

export const BOT_PERMISSION_ORDER: BotPermission[] = [
  'chat',
  'workspace_read',
  'workspace_write',
  'workspace_exec',
  'manage',
]

export function expandBotPermissions(permissions: readonly string[] | null | undefined): BotPermission[] {
  const seen = new Set<BotPermission>()
  for (const permission of permissions ?? []) {
    if (BOT_PERMISSION_ORDER.includes(permission as BotPermission)) {
      seen.add(permission as BotPermission)
    }
  }
  if (seen.has('manage')) {
    for (const permission of BOT_PERMISSION_ORDER) seen.add(permission)
  }
  if (seen.has('workspace_write')) {
    seen.add('workspace_read')
  }
  return BOT_PERMISSION_ORDER.filter(permission => seen.has(permission))
}

export function hasBotPermission(permissions: readonly string[] | null | undefined, permission: BotPermission): boolean {
  return expandBotPermissions(permissions).includes(permission)
}

/**
 * Only restrict management when the backend explicitly reports a scoped
 * permission set without "manage" (e.g. chat-only members). When permissions are
 * absent or empty (older backend/cache), keep management enabled and let the
 * backend enforce access.
 */
export function canManageBotPermissions(permissions: readonly string[] | null | undefined): boolean {
  if (!permissions || permissions.length === 0) return true
  return hasBotPermission(permissions, 'manage')
}
