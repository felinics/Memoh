import type { HandlersContextUsage } from '@memohai/sdk'
import { computeContextComposition, type ContextComposition } from './context-categories'

export interface SessionContextView {
  composition: ContextComposition | null
  estimatedTokens: number | null
  contextWindow: number | null
  outputReserve: number | null
  autoCompactTokens: number | null
  hardCompactTokens: number | null
  compactionAvailable: boolean
}

export interface SessionContextViewOptions {
  // A pane-level model override budgets the next turn against another model,
  // so the last turn's plan (window, reserve, marks) no longer applies.
  overrideActive: boolean
  fallbackWindow: number | null | undefined
}

function positive(value: number | null | undefined): number | null {
  return value != null && value > 0 ? value : null
}

// The fragment estimate is the basis the backend budgets and compacts on, and
// the only one ACP sessions report; the plan window is the denominator the turn
// actually ran against, so it wins over the resolved model window.
export function resolveSessionContextView(
  usage: HandlersContextUsage | null | undefined,
  options: SessionContextViewOptions,
): SessionContextView {
  const composition = computeContextComposition(usage)
  const plan = options.overrideActive ? undefined : usage?.budget_plan
  const compaction = usage?.compaction
  const marksApply = plan != null && compaction?.enabled === true
  return {
    composition,
    estimatedTokens: composition?.totalTokens ?? null,
    contextWindow: positive(plan?.window) ?? positive(usage?.context_window) ?? positive(options.fallbackWindow),
    outputReserve: positive(plan?.output_reserve),
    autoCompactTokens: marksApply ? positive(compaction.auto_tokens) : null,
    hardCompactTokens: marksApply ? positive(compaction.hard_tokens) : null,
    compactionAvailable: compaction != null,
  }
}
