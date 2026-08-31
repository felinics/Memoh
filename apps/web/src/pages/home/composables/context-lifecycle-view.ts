import type { ContextfragLifecycleSnapshot, ContextfragSelectionDecision } from '@memohai/sdk'
import { computeContextComposition, type ContextComposition } from './context-categories'

export function compositionFromSnapshot(snapshot: ContextfragLifecycleSnapshot | null | undefined): ContextComposition | null {
  const composition = computeContextComposition({ breakdown: snapshot?.breakdown, tool_defs: snapshot?.tool_defs })
  return composition && composition.categories.length > 0 ? composition : null
}

export interface DropReasonGroup {
  reason: string
  count: number
  tokens: number
}

export function groupDroppedDecisions(decisions: ContextfragSelectionDecision[] | null | undefined): DropReasonGroup[] {
  if (!decisions) return []

  const groups = new Map<string, DropReasonGroup>()
  for (const entry of decisions) {
    if (entry.decision === 'selected') continue
    const reason = entry.reason?.trim() || entry.decision || 'unknown'
    const group = groups.get(reason) ?? { reason, count: 0, tokens: 0 }
    group.count += 1
    group.tokens += entry.token_estimate ?? 0
    groups.set(reason, group)
  }

  return [...groups.values()].sort((a, b) => b.tokens - a.tokens || b.count - a.count || a.reason.localeCompare(b.reason))
}

// Every class is a literal so the Tailwind scanner can see it.
export function lifecycleStatusToneClass(status: string | null | undefined): string {
  switch (status) {
    case 'completed': return 'text-muted-foreground'
    case 'fallback': return 'text-warning'
    case 'failed_budget':
    case 'failed_provider':
    case 'aborted': return 'text-destructive'
    default: return 'text-muted-foreground'
  }
}

export function lifecycleStatusLabelKey(status: string | null | undefined): string | null {
  switch (status) {
    case 'completed': return 'chat.lifecycle.statusCompleted'
    case 'fallback': return 'chat.lifecycle.statusFallback'
    case 'failed_budget': return 'chat.lifecycle.statusFailedBudget'
    case 'failed_provider': return 'chat.lifecycle.statusFailedProvider'
    case 'aborted': return 'chat.lifecycle.statusAborted'
    default: return null
  }
}
