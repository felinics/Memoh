import type { ContentBlock, ErrorBlock } from '@/store/chat/types'

// Stable runtime feedback code for a workspace dependency the agent needs but
// the workspace does not have (design §9.4): the Server rejects the turn and
// starts the install in the background.
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
