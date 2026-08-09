export const REASONING_EFFORT_DISABLE = 'disable'
// Legacy override value. "adaptive" is no longer an effort tier the UI offers —
// it is a thinking mode handled server-side. The constant is
// kept so previously-stored values still render gracefully.
export const REASONING_EFFORT_ADAPTIVE = 'adaptive'

export type ThinkingMode = 'toggle' | 'adaptive' | 'only_adaptive' | 'none'

// Active effort tiers the UI understands, ordered weakest → strongest. "off" is
// REASONING_EFFORT_DISABLE and deliberately absent: it is not a tier, and keeping
// it out is what stops the nearest-tier fallback from resolving to "off".
//
// Keep in sync with orderedReasoningEfforts in internal/models/types.go.
export const KNOWN_EFFORTS = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max'] as const

// Values a model may advertise: the active tiers plus the disable token, which
// declares that the model can be turned off. OpenAI's wire spelling of off
// ("none") is not declarable — provider adaptors translate into it.
//
// Keep in sync with validReasoningEfforts in internal/models/types.go.
const DECLARABLE_EFFORTS: readonly string[] = [REASONING_EFFORT_DISABLE, ...KNOWN_EFFORTS]

// How "off" was spelled in declarations before the two tokens were unified.
// Configs written under the old spelling are rewritten on read, not discarded.
//
// Keep in sync with normalizeAdvertisedEfforts in internal/models/types.go.
const LEGACY_OFF_EFFORT = 'none'

// Keep in sync with normalizesMaxReasoningEffort in internal/conversation/flow/resolver.go.
// Generic OpenAI-format clients retain the existing max-to-xhigh compatibility
// behavior; Codex uses the effort levels advertised by its catalog directly.
const MAX_NORMALIZED_CLIENT_TYPES = new Set(['openai-completions', 'openai-responses'])

// Clients whose wire format expresses "off" by omission rather than by a value.
// Anthropic's Messages API has no off token: thinking is off when the field is
// absent, which is exactly what the adaptor sends for a disabled toggle model.
// No catalog advertises the disable token for Claude either — the capability
// registry never marks an Anthropic entry as supporting the off effort — so
// gating on the declaration alone would hide a control that works.
const IMPLICIT_OFF_CLIENT_TYPES = new Set(['anthropic-messages'])

export const EFFORT_LABELS: Record<string, string> = {
  [REASONING_EFFORT_DISABLE]: 'chat.reasoningOff',
  [REASONING_EFFORT_ADAPTIVE]: 'chat.reasoningAdaptive',
  // Legacy spelling of off, kept so values stored before the two tokens were
  // unified still render — under the same label, not a second one.
  none: 'chat.reasoningOff',
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
  none: 0.1,
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

// resolveEffortLevels returns what the model advertises — its active tiers, plus
// the disable token when the model can be turned off — falling back to the common
// low/medium/high subset when nothing is advertised. Generic OpenAI-format clients
// use xhigh as the highest effort tier, while Codex can expose max directly.
//
// The disable token stays in the result because that is how the picker learns
// whether "off" is achievable at all. It is dropped from the tier ordering
// instead (see KNOWN_EFFORTS), so it can never be chosen as an active effort.
//
// The legacy spelling is rewritten rather than filtered out. A config that still
// advertises "none" describes a model that can be turned off, so dropping it would
// hide Off from a model that supports it — for as long as that row goes unwritten.
export function resolveEffortLevels(config?: ModelConfigLike | null, clientType?: string | null): string[] {
  const advertised = (config?.reasoning_efforts ?? [])
    .map(e => (e === LEGACY_OFF_EFFORT ? REASONING_EFFORT_DISABLE : e))
  const efforts = advertised.filter((e, i) => DECLARABLE_EFFORTS.includes(e) && advertised.indexOf(e) === i)
  const levels = efforts.length > 0 ? efforts : ['low', 'medium', 'high']
  if (MAX_NORMALIZED_CLIENT_TYPES.has(clientType ?? '')) {
    return levels.filter((e) => e !== 'max')
  }
  return levels
}

// nearestEffortToMedium picks the tier closest to medium from levels, breaking
// ties toward the weaker tier. It is the fallback when a model does not
// advertise medium: [minimal, low] -> low, [high, max] -> high, [low, high] -> low.
// Values outside KNOWN_EFFORTS — including the disable token, which a model may
// advertise — are ignored, so passing a selectable list straight in never yields
// "off"; returns '' when no usable tier is present.
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

// availableEffortsForMode builds the selectable list for a thinking mode. A model
// with no thinking concept offers nothing; otherwise the options are the model's
// active tiers, with "off" hoisted to the front when off is actually reachable.
//
// Off is no longer prepended unconditionally. Doing so offered it on models that
// cannot be turned off, where picking it has no defined effect: the adaptor has no
// off shape to send, so it omits the field and the model's default thinking stands.
export function availableEffortsForMode(mode: ThinkingMode, levels: string[], clientType?: string | null): string[] {
  if (mode === 'none') return []
  const tiers = levels.filter((e) => e !== REASONING_EFFORT_DISABLE)
  return canTurnOff(mode, levels, clientType) ? [REASONING_EFFORT_DISABLE, ...tiers] : tiers
}

// canTurnOff reports whether picking "off" reaches the model: either the model
// advertises the token, or its client expresses off by omitting the field.
//
// The omission rule is limited to toggle models, where thinking only happens when
// asked for. Under adaptive thinking the model decides on its own, so omitting the
// field leaves that default in charge rather than turning thinking off.
function canTurnOff(mode: ThinkingMode, levels: string[], clientType?: string | null): boolean {
  if (levels.includes(REASONING_EFFORT_DISABLE)) return true
  return mode === 'toggle' && IMPLICIT_OFF_CLIENT_TYPES.has(clientType ?? '')
}
