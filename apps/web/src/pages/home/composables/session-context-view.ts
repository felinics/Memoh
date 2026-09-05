import type { HandlersContextUsage } from '@memohai/sdk'
import { computeContextComposition, positive, type ContextComposition } from './context-categories'

export interface SessionContextView {
  composition: ContextComposition | null
  estimatedTokens: number | null
  contextWindow: number | null
  outputReserve: number | null
  autoCompactTokens: number | null
  compactionAvailable: boolean
}

export interface SessionContextViewOptions {
  fallbackWindow: number | null | undefined
}

// The fragment estimate is the basis the backend budgets and compacts on, and
// the only one ACP sessions report; the plan window is the denominator the turn
// actually ran against, so it wins over the resolved model window. The status
// already omits the plan when the next turn targets another model.
export function resolveSessionContextView(
  usage: HandlersContextUsage | null | undefined,
  options: SessionContextViewOptions,
): SessionContextView {
  const composition = computeContextComposition(usage)
  const plan = usage?.budget_plan
  const compaction = usage?.compaction
  const markApplies = plan != null && compaction?.enabled === true
  return {
    composition,
    estimatedTokens: composition?.totalTokens ?? null,
    contextWindow: positive(plan?.window) ?? positive(usage?.context_window) ?? positive(options.fallbackWindow),
    outputReserve: positive(plan?.output_reserve),
    autoCompactTokens: markApplies ? positive(compaction.auto_tokens) : null,
    compactionAvailable: compaction != null,
  }
}
