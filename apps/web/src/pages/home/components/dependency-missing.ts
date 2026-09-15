import type { ContentBlock, ErrorBlock } from '@/store/chat/types'

// Stable runtime feedback code for a workspace dependency the agent needs but
// the workspace does not have. The turn stops; any admitted recovery is a
// separate operation bound to an existing manager authorization.
export const AGENT_DEPENDENCY_MISSING_CODE = 'agent_dependency_missing'

// Deliberately not a type predicate: the template's later `v-else-if` branch
// still renders ordinary error blocks, which a narrowing guard would exclude.
export function isDependencyMissingBlock(block: ContentBlock): boolean {
  return block.type === 'error' && block.code?.trim() === AGENT_DEPENDENCY_MISSING_CODE
}

/** Trimmed string args; missing or blank entries are dropped. */
export function dependencyMissingArgs(block: ErrorBlock): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [key, value] of Object.entries(block.args ?? {})) {
    const trimmed = typeof value === 'string' ? value.trim() : ''
    if (trimmed) out[key] = trimmed
  }
  return out
}

/** Only an accepted operation may be described as installing. */
export function dependencyInstallationInProgress(args: Record<string, string>): boolean {
  return !!args.install_task_id || args.operation_in_progress === 'true'
}

/** Feedback records an accepted recovery state, not permission to retry a turn. */
export function dependencyMissingMessageKey(args: Record<string, string>): string {
  if (args.desired_version) {
    if ((args.repair_status === 'queued' || args.repair_status === 'installing') && args.repair_operation_id) {
      return `chat.externalAgent.dependencyRepair.${args.repair_status}`
    }
    if (args.repair_status === 'backoff' || args.repair_status === 'manual_required') {
      return `chat.externalAgent.dependencyRepair.${args.repair_status}`
    }
  }
  return dependencyInstallationInProgress(args)
    ? 'chat.externalAgent.dependencyMissingInstalling'
    : 'chat.externalAgent.dependencyMissing'
}
