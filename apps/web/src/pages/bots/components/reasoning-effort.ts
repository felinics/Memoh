export const REASONING_EFFORT_DISABLE = 'disable'
// Legacy override value. "adaptive" is no longer an effort tier the UI offers —
// it is a thinking mode handled server-side. The constant is
// kept so previously-stored values still render gracefully.
export const REASONING_EFFORT_ADAPTIVE = 'adaptive'

export type ThinkingMode = 'toggle' | 'adaptive' | 'only_adaptive' | 'none'

// Effort tiers the UI understands, ordered weakest → strongest.
export const KNOWN_EFFORTS = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'] as const

// Keep in sync with normalizesMaxReasoningEffort in internal/conversation/flow/resolver.go.
// Generic OpenAI-format clients retain the existing max-to-xhigh compatibility
// behavior; Codex uses the effort levels advertised by its catalog directly.
const MAX_NORMALIZED_CLIENT_TYPES = new Set(['openai-completions', 'openai-responses'])

export const EFFORT_LABELS: Record<string, string> = {
  [REASONING_EFFORT_DISABLE]: 'chat.reasoningOff',
  [REASONING_EFFORT_ADAPTIVE]: 'chat.reasoningAdaptive',
  none: 'chat.reasoningNone',
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
  none: 0.15,
  minimal: 0.25,
  low: 0.4,
  medium: 0.6,
  high: 0.8,
  xhigh: 0.92,
  max: 1,
}

interface ModelConfigLike {
  thinking_mode?: string
  reasoning_efforts?: string[]
  compatibilities?: string[]
}

// resolveThinkingMode derives the effective thinking mode from a model config,
// with a legacy fallback for models imported before thinking_mode existed:
// the old "reasoning" compatibility maps to toggle, its absence to none.
export function resolveThinkingMode(config?: ModelConfigLike | null): ThinkingMode {
  const mode = config?.thinking_mode
  if (mode === 'toggle' || mode === 'adaptive' || mode === 'none') {
    return mode
  }
  if (mode === 'only_adaptive') return 'adaptive'
  return config?.compatibilities?.includes('reasoning') ? 'toggle' : 'none'
}

// resolveEffortLevels returns the model's supported effort tiers (filtered to
// known ones), falling back to the common low/medium/high subset. Generic
// OpenAI-format clients use xhigh as the highest effort tier, while Codex can
// expose max directly.
export function resolveEffortLevels(config?: ModelConfigLike | null, clientType?: string | null): string[] {
  const efforts = (config?.reasoning_efforts ?? []).filter((e) =>
    (KNOWN_EFFORTS as readonly string[]).includes(e),
  )
  const levels = efforts.length > 0 ? efforts : ['low', 'medium', 'high']
  if (MAX_NORMALIZED_CLIENT_TYPES.has(clientType ?? '')) {
    return levels.filter((e) => e !== 'max')
  }
  return levels
}

// nearestEffortToMedium picks the tier closest to medium from levels, breaking
// ties toward the weaker tier. It is the fallback when a model does not
// advertise medium: [minimal, low] -> low, [high, max] -> high, [low, high] -> low.
// Values outside KNOWN_EFFORTS (including the "disable" sentinel that
// availableEffortsForMode prepends) are ignored, so passing a selectable list
// straight in never yields "off"; returns '' when no usable tier is present.
//
// Keep in sync with NearestEffortToMedium in internal/models/types.go.
export function nearestEffortToMedium(levels: string[]): string {
  const order = KNOWN_EFFORTS as readonly string[]
  const mediumIdx = order.indexOf('medium')

  let best = ''
  let bestIdx = -1
  let bestDistance = 0
  for (const level of levels) {
    const idx = order.indexOf(level)
    if (idx < 0) continue
    const distance = Math.abs(idx - mediumIdx)
    // Ties break toward the weaker tier rather than toward whichever came first,
    // because levels arrives in registry order and is not guaranteed sorted.
    if (best === '' || distance < bestDistance || (distance === bestDistance && idx < bestIdx)) {
      best = level
      bestIdx = idx
      bestDistance = distance
    }
  }
  return best
}

// availableEffortsForMode builds the selectable list for a thinking mode:
//   - none:          nothing
//   - adaptive/toggle: an explicit "off" plus the effort tiers
export function availableEffortsForMode(mode: ThinkingMode, levels: string[]): string[] {
  if (mode === 'none') return []
  return [REASONING_EFFORT_DISABLE, ...levels]
}
