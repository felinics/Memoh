import type { HandlersContextUsage } from '@memohai/sdk'
import { computeContextComposition, positive, type ContextComposition } from './context-categories'

export interface SessionContextView {
  composition: ContextComposition | null
  estimatedTokens: number | null
  // usedTokens is null when an External Agent runtime's newest context state
  // is unknown; native sessions report zero before their first turn.
  usedTokens: number | null
  runtimeObserved: boolean
  contextWindow: number | null
  outputReserve: number | null
  autoCompactTokens: number | null
  compactionAvailable: boolean
}

export interface SessionContextViewOptions {
  fallbackWindow: number | null | undefined
}

// The fragment estimate is the basis the backend budgets and compacts on for
// native sessions; the plan window is the denominator the turn actually ran
// against, so it wins over the resolved model window. The status already omits
// the plan when the next turn targets another model. An External Agent
// runtime reports its own measurement and window instead: neither Memoh's
// context-document estimate nor another model's window stands in for them.
export function resolveSessionContextView(
  usage: HandlersContextUsage | null | undefined,
  options: SessionContextViewOptions,
): SessionContextView {
  if (usage?.basis === 'runtime') {
    const usedTokens = usage.used_tokens ?? null
    return {
      composition: null,
      estimatedTokens: null,
      usedTokens,
      runtimeObserved: true,
      contextWindow: usedTokens == null ? null : positive(usage.context_window),
      outputReserve: null,
      autoCompactTokens: null,
      compactionAvailable: usage.compaction != null,
    }
  }
  const composition = computeContextComposition(usage)
  const plan = usage?.budget_plan
  const compaction = usage?.compaction
  const markApplies = plan != null && compaction?.enabled === true
  return {
    composition,
    estimatedTokens: composition?.totalTokens ?? null,
    usedTokens: usage?.used_tokens ?? 0,
    runtimeObserved: false,
    contextWindow: positive(plan?.window) ?? positive(usage?.context_window) ?? positive(options.fallbackWindow),
    outputReserve: positive(plan?.output_reserve),
    autoCompactTokens: markApplies ? positive(compaction.auto_tokens) : null,
    compactionAvailable: compaction != null,
  }
}
