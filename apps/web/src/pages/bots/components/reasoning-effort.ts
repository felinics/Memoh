import type { ReasoningOptions } from '@memohai/sdk'

export const REASONING_EFFORT_DISABLE = 'disable'
// Legacy override value. "adaptive" is no longer an effort tier the UI offers —
// it is a thinking mode handled server-side. The constant is
// kept so previously-stored values still render gracefully.
export const REASONING_EFFORT_ADAPTIVE = 'adaptive'
// How off was spelled before the two tokens were unified, and still OpenAI's wire
// word for it. Stored values may carry it, so it has to render and reconcile as
// off — naming it keeps that intent visible wherever it appears.
export const REASONING_EFFORT_LEGACY_OFF = 'none'

export const EFFORT_LABELS: Record<string, string> = {
  [REASONING_EFFORT_DISABLE]: 'chat.reasoningOff',
  [REASONING_EFFORT_ADAPTIVE]: 'chat.reasoningAdaptive',
  // Under the same label as off, not a second one.
  [REASONING_EFFORT_LEGACY_OFF]: 'chat.reasoningOff',
  minimal: 'chat.reasoningMinimal',
  low: 'chat.reasoningLow',
  medium: 'chat.reasoningMedium',
  high: 'chat.reasoningHigh',
  xhigh: 'chat.reasoningXHigh',
  max: 'chat.reasoningMax',
}

export const EFFORT_OPACITY: Record<string, number> = {
  [REASONING_EFFORT_DISABLE]: 0.1,
  [REASONING_EFFORT_ADAPTIVE]: 0.25,
  [REASONING_EFFORT_LEGACY_OFF]: 0.1,
  minimal: 0.25,
  low: 0.4,
  medium: 0.6,
  high: 0.8,
  xhigh: 0.92,
  max: 1,
}

// selectableEfforts turns the server's resolved options into the picker's list:
// the model's active tiers, with "off" hoisted to the front when off is actually
// reachable. A model with no thinking concept offers nothing.
//
// Whether off is reachable is the server's `can_disable`, not something read out
// of the tier list. It forks by provider family — Claude expresses off by
// omitting the field and never advertises a token for it — which is why the
// frontend no longer tries to answer it.
export function selectableEfforts(options?: ReasoningOptions | null): string[] {
  if (!options?.supported) return []
  const tiers = options.efforts ?? []
  return options.can_disable ? [REASONING_EFFORT_DISABLE, ...tiers] : tiers
}

// reconcileStoredEffort returns the effort to hold after the model changed. A
// stored value the model still offers survives; "off" survives when off is still
// reachable; anything else lands on the model's default tier. It returns '' when
// the model has no thinking concept, meaning the caller should clear the value.
//
// This mirrors reasoning.ReconcileStored in the Go backend, which owns the same
// policy for the /reasoning command. It is a migration rule rather than a
// capability derivation: every input it reads was decided server-side.
export function reconcileStoredEffort(stored: string, options?: ReasoningOptions | null): string {
  if (!options?.supported) return ''
  const tiers = options.efforts ?? []
  if (!options.can_disable && tiers.length === 0) {
    // Always-on models expose no selection to migrate to. Keep the preference
    // dormant so switching back restores it and the settings autosave does not
    // oscillate between an empty local value and the stored server value.
    return stored === REASONING_EFFORT_LEGACY_OFF ? REASONING_EFFORT_DISABLE : stored
  }
  const fallback = options.default_effort ?? ''
  // Both spellings of off are honoured: rows written before the two tokens were
  // unified still say "none", and they describe the same state.
  if (stored === REASONING_EFFORT_DISABLE || stored === REASONING_EFFORT_LEGACY_OFF) {
    return options.can_disable ? REASONING_EFFORT_DISABLE : fallback
  }
  return tiers.includes(stored) ? stored : fallback
}
