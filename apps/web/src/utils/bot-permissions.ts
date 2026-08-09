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
 * Peer scopes answer "may this bot reach this bot", a different question from
 * the user scopes above. The two vocabularies are disjoint on purpose — the
 * backend stores them in separate tables and rejects any crossover — so they
 * get separate types here rather than a widened union that would let a caller
 * pass 'manage' where a peer scope is expected.
 */
export type BotPeerPermission =
  | 'discover'
  | 'contact'
  | 'delegate'

export const BOT_PEER_PERMISSION_ORDER: BotPeerPermission[] = [
  'discover',
  'contact',
  'delegate',
]

/** delegate implies contact implies discover. */
const BOT_PEER_PERMISSION_IMPLIES: Record<BotPeerPermission, BotPeerPermission[]> = {
  discover: [],
  contact: ['discover'],
  delegate: ['contact', 'discover'],
}

export function expandBotPeerPermissions(permissions: readonly string[] | null | undefined): BotPeerPermission[] {
  const seen = new Set<BotPeerPermission>()
  for (const permission of permissions ?? []) {
    if (!BOT_PEER_PERMISSION_ORDER.includes(permission as BotPeerPermission)) continue
    const scope = permission as BotPeerPermission
    seen.add(scope)
    for (const implied of BOT_PEER_PERMISSION_IMPLIES[scope]) seen.add(implied)
  }
  return BOT_PEER_PERMISSION_ORDER.filter(permission => seen.has(permission))
}
