import type { HandlersTriggerCompactResponse } from '@memohai/sdk'

const heldBackReasons = new Set(['no_beneficial_span', 'read_budget_exceeded'])

// A noop is not a success: tell nothing-to-do apart from history that
// compaction cannot shrink, and from a model that returned no usable summary.
// Returns undefined when a summary was committed.
export function compactionNoticeKey(outcome: HandlersTriggerCompactResponse | undefined) {
  if (outcome?.status !== 'noop') return undefined
  if (outcome.reason === 'summary_unusable') return 'chat.compactUnusable'
  return heldBackReasons.has(outcome.reason ?? '') ? 'chat.compactBlocked' : 'chat.compactNothing'
}
