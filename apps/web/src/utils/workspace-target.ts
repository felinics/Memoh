import type { WorkspaceWorkspaceTarget } from '@memohai/sdk'

// Display + availability rules for workspace targets, shared by the composer
// Continue-on selector and chat-pane's selection guard. All label helpers take
// `t` because i18n is only available inside setup scope.

type I18nT = (key: string, ...args: unknown[]) => string

export function workspaceTargetName(target: Pick<WorkspaceWorkspaceTarget, 'kind' | 'name'>, t: I18nT): string {
  if (target.kind === 'native') return t('bots.remoteRuntime.nativeWorkspace')
  return target.name || t('bots.remoteRuntime.unknownComputer')
}

export function workspaceTargetStatus(target: WorkspaceWorkspaceTarget): string {
  if (target.kind === 'native') return 'online'
  return target.status || (target.online ? 'online' : 'offline')
}

export function workspaceTargetStatusLabel(target: WorkspaceWorkspaceTarget, t: I18nT): string {
  const status = workspaceTargetStatus(target)
  const key = `runtimes.status.${status}`
  const label = t(key)
  return label === key ? status : label
}

export function workspaceTargetAvailable(target: WorkspaceWorkspaceTarget): boolean {
  return target.kind === 'native'
    || (workspaceTargetStatus(target) === 'online' && target.online !== false)
}
