import type { AcpagentRuntimeStatus, AcpclientAvailableCommandInfo } from '@memohai/sdk'

export type ACPAvailableCommand = AcpclientAvailableCommandInfo
export type VisibleACPAvailableCommand = ACPAvailableCommand & { name: string }

const RESERVED_MEMOH_ACP_SLASH_NAMES = new Set([
  'help',
  'new',
  'permission',
  'skill',
])

function validACPSlashCommandName(name: string | undefined): string {
  if (!name || name.trim() !== name || name.startsWith('/') || /\s/.test(name)) return ''
  return name
}

export function acpRuntimeMatchesConfiguration(
  runtime: AcpagentRuntimeStatus | undefined,
  agentId: string,
  projectPath: string,
): boolean {
  const expectedAgent = agentId.trim()
  const expectedProjectPath = projectPath.trim()
  return Boolean(
    runtime
    && expectedAgent
    && runtime.agent_id?.trim() === expectedAgent
    && (!expectedProjectPath || runtime.project_path?.trim() === expectedProjectPath),
  )
}

export function isBoundACPRuntimeForTarget(
  runtime: AcpagentRuntimeStatus | undefined,
  target: { sessionId: string, agentId: string, projectPath: string },
): boolean {
  const sessionId = target.sessionId.trim()
  return Boolean(
    sessionId
    && runtime?.session_id?.trim() === sessionId
    && runtime.acp_session_id?.trim()
    && acpRuntimeMatchesConfiguration(runtime, target.agentId, target.projectPath),
  )
}

export function visibleACPSlashCommands(
  commands: readonly ACPAvailableCommand[] | undefined,
  query: string,
): VisibleACPAvailableCommand[] {
  const seen = new Set<string>()
  const normalizedQuery = query.trim().toLowerCase()
  const visible: VisibleACPAvailableCommand[] = []

  for (const command of commands ?? []) {
    const name = validACPSlashCommandName(command.name)
    if (!name || RESERVED_MEMOH_ACP_SLASH_NAMES.has(name.toLowerCase()) || seen.has(name)) continue
    seen.add(name)

    const description = command.description?.trim() ?? ''
    if (normalizedQuery && !`${name} ${description}`.toLowerCase().includes(normalizedQuery)) continue
    visible.push({
      ...command,
      name,
      description: description || undefined,
      input_hint: command.input_hint?.trim() || undefined,
    })
  }

  return visible
}

export function acpSlashCommandComposerText(command: ACPAvailableCommand): string {
  const name = validACPSlashCommandName(command.name)
  if (!name) return ''
  return `/${name}${command.input_hint?.trim() ? ' ' : ''}`
}

export function composerLocalQuickActionID(
  text: string,
  usesExternalAgentComposer: boolean,
): '' | 'compact' | 'model' {
  if (usesExternalAgentComposer) return ''
  switch (text.trim().toLowerCase()) {
    case '/compact':
      return 'compact'
    case '/model':
    case '/models':
      return 'model'
    default:
      return ''
  }
}
