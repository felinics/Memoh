import type { TurnRuntimeCommand } from '@memohai/sdk'

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
  goalSupported = false,
): '' | 'compact' | 'model' | 'plan' | 'goal' {
  if (goalSupported && text.trim().toLowerCase() === '/goal') return 'goal'
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
