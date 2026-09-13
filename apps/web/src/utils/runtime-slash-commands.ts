import type { TurnRuntimeCommand } from '@memohai/sdk'
import { BOT_AGENT_RUNTIME_CLAUDE_CODE, BOT_AGENT_RUNTIME_CODEX } from './bot-agent'

export type RuntimeCommand = TurnRuntimeCommand
export type VisibleRuntimeCommand = RuntimeCommand & { name: string }

const RESERVED_MEMOH_SLASH_NAMES = new Set([
  'help',
  'new',
  'permission',
  'skill',
])

function validRuntimeCommandName(name: string | undefined): string {
  if (!name || name.trim() !== name || name.startsWith('/') || /\s/.test(name)) return ''
  return name
}

export function visibleRuntimeCommands(
  commands: readonly RuntimeCommand[] | undefined,
  query: string,
): VisibleRuntimeCommand[] {
  const seen = new Set<string>()
  const normalizedQuery = query.trim().toLowerCase()
  const visible: VisibleRuntimeCommand[] = []

  for (const command of commands ?? []) {
    const name = validRuntimeCommandName(command.name)
    if (!name || RESERVED_MEMOH_SLASH_NAMES.has(name.toLowerCase()) || seen.has(name)) continue
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

export function runtimeCommandComposerText(command: RuntimeCommand): string {
  const name = validRuntimeCommandName(command.name)
  if (!name) return ''
  return `/${name}${command.input_hint?.trim() ? ' ' : ''}`
}

export function composerLocalQuickActionID(
  text: string,
  usesExternalAgentComposer: boolean,
  planModeSupported = false,
  goalShortcutSupported = false,
): '' | 'compact' | 'model' | 'plan' | 'goal' {
  if (goalShortcutSupported && text.trim().toLowerCase() === '/goal') return 'goal'
  if (planModeSupported && text.trim().toLowerCase() === '/plan') return 'plan'
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

// Native Goal grammars differ; control commands are not objective messages.
export function runtimeGoalObjective(text: string, runtime?: string): string | null {
  const objective = /^\/goal\s+(.+)$/s.exec(text.trim())?.[1]?.trim()
  if (!objective) return null
  if (runtime === BOT_AGENT_RUNTIME_CODEX) return objective === 'resume' ? null : objective
  if (runtime === BOT_AGENT_RUNTIME_CLAUDE_CODE) {
    return ['clear', 'stop', 'off', 'reset', 'none', 'cancel'].includes(objective.toLowerCase()) ? null : objective
  }
  return null
}
