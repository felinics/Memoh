import type { ContextfragLifecycleSnapshot } from '@memohai/sdk'
import { formatTokenCount } from './context-categories'
import { dropReasonRows } from './context-lifecycle-view'
import type { ContextEntry, RowMapSegment, TimelineLane, TrajectoryRow, TrajectoryRowKind, TrajectoryStats } from './trajectory-model'

export type TimelineMode = 'duration' | 'sequence'

export interface RowMapBar {
  key: string
  rowKey: string
  lane: TimelineLane
  kind: TrajectoryRowKind
  leftPct: number
  widthPct: number
  // Where the first token landed inside a model bar, as a percentage of the
  // bar's own width; null when the request never streamed a token.
  splitPct: number | null
  label: string
  durationMs: number
  running: boolean
  turnStart: boolean
}

const MIN_BAR_PCT = 1
const TURN_GAP_PCT = 0.6

// Segments keep ledger order. Duration mode scales each by its own wall
// time with a floor so timeless rows stay visible; sequence mode gives every
// segment the same width. Turns are separated by a small gap.
export function rowMapGeometry(segments: RowMapSegment[], mode: TimelineMode): RowMapBar[] {
  if (segments.length === 0) return []
  const gaps = segments.filter((segment, index) => index > 0 && segment.turnStart).length
  const available = 100 - gaps * TURN_GAP_PCT
  const durations = segments.map(segment => Math.max(segment.durationMs, 0))
  const total = durations.reduce((sum, value) => sum + value, 0)
  const weights = mode === 'duration' && total > 0 ? durations.map(value => value / total) : segments.map(() => 1 / segments.length)
  const floored = weights.map(weight => Math.max(weight * available, MIN_BAR_PCT))
  const scale = available / floored.reduce((sum, value) => sum + value, 0)
  let cursor = 0
  return segments.map((segment, index) => {
    if (index > 0 && segment.turnStart) cursor += TURN_GAP_PCT
    const widthPct = floored[index]! * scale
    const leftPct = cursor
    cursor += widthPct
    const splitPct = segment.splitMs != null && segment.durationMs > 0
      ? Math.min(Math.max((segment.splitMs / segment.durationMs) * 100, 0), 100)
      : null
    return {
      key: segment.key,
      rowKey: segment.rowKey,
      lane: segment.lane,
      kind: segment.kind,
      leftPct,
      widthPct,
      splitPct,
      label: segment.label,
      durationMs: segment.durationMs,
      running: segment.running,
      turnStart: segment.turnStart,
    }
  })
}

export function formatDurationMs(ms: number): string {
  const value = Math.max(ms, 0)
  if (value < 1_000) return `${Math.round(value)}ms`
  if (value < 60_000) return `${(value / 1_000).toFixed(1)}s`
  const minutes = Math.floor(value / 60_000)
  const seconds = Math.round((value - minutes * 60_000) / 1_000)
  return `${minutes}m ${seconds}s`
}

type Translate = (key: string, params?: Record<string, unknown>) => string

export function contextLabelKey(entry: ContextEntry): string | null {
  switch (entry.kind) {
    case 'fragments':
      return `chat.trajectory.contextKind.${entry.fragmentKind}`
    case 'mutation':
      return null
    default:
      return `chat.trajectory.contextKind.${entry.kind}`
  }
}

// One line per context entry from its own numbers; mutations quote the
// detail the runtime recorded because their kinds are already the label.
export function contextPreview(entry: ContextEntry, t: Translate): string {
  switch (entry.kind) {
    case 'fragments':
      if (entry.selection) {
        const dropped = (entry.selection.dropped ?? 0) + (entry.selection.trimmed ?? 0)
        return dropped > 0
          ? t('chat.trajectory.contextHistoryCut', { messages: entry.fragments, tokens: formatTokenCount(entry.tokens), dropped })
          : t('chat.trajectory.contextHistory', { messages: entry.fragments, tokens: formatTokenCount(entry.tokens) })
      }
      return t('chat.trajectory.contextFragments', { fragments: entry.fragments, tokens: formatTokenCount(entry.tokens) })
    case 'tool_defs':
      return t('chat.trajectory.contextToolDefs', { n: entry.tools, tokens: formatTokenCount(entry.tokens) })
    case 'memory_recall':
      return t('chat.trajectory.contextMemory', { count: entry.memory.result?.count ?? 0, state: entry.memory.cache_state ?? '' })
    case 'selection':
      return t('chat.trajectory.contextSelection', { selected: entry.selection.selected ?? 0, dropped: entry.selection.dropped ?? 0, trimmed: entry.selection.trimmed ?? 0 })
    case 'mutation':
      return entry.mutation.detail?.trim() ?? ''
    case 'step':
      return t('chat.trajectory.contextStep', { dropped: entry.step.dropped ?? 0, truncated: entry.step.truncated ?? 0, outcome: entry.step.reselection_outcome?.trim() ?? '' })
  }
}

export interface LabeledRow {
  key: string
  label: string
  value: string
  mono?: boolean
}

function labeled(t: Translate, rows: [string, string, string | number | null | undefined][]): LabeledRow[] {
  return rows.flatMap(([key, labelKey, value]) => {
    if (value == null || value === '' || value === 0) return []
    return [{ key, label: t(labelKey), value: typeof value === 'number' ? formatTokenCount(value) : value }]
  })
}

// Fixed facts of a context entry, in the inspector's label/value shape.
export function contextDetailRows(entry: ContextEntry, t: Translate): LabeledRow[] {
  switch (entry.kind) {
    case 'fragments':
      return labeled(t, [
        ['fragments', 'chat.trajectory.inspectorFragments', entry.fragments],
        ['tokens', 'chat.trajectory.inspectorTokens', entry.tokens],
        ['bytes', 'chat.trajectory.inspectorBytes', entry.textBytes],
        ['images', 'chat.trajectory.inspectorImages', entry.images],
        ['dropped', 'chat.trajectory.inspectorDropped', entry.selection?.dropped],
        ['trimmed', 'chat.trajectory.inspectorTrimmed', entry.selection?.trimmed],
        ...(entry.memory ? memoryRows(entry.memory) : []),
      ])
    case 'memory_recall':
      return labeled(t, memoryRows(entry.memory))
    case 'tool_defs':
      return labeled(t, [
        ['tools', 'chat.trajectory.inspectorTools', String(entry.tools)],
        ['tokens', 'chat.trajectory.inspectorTokens', entry.tokens],
        ['providers', 'chat.trajectory.inspectorProviders', entry.providers.join(', ')],
      ])
    case 'selection':
      return labeled(t, [
        ['selected', 'chat.trajectory.inspectorSelected', entry.selection.selected],
        ['dropped', 'chat.trajectory.inspectorDropped', entry.selection.dropped],
        ['trimmed', 'chat.trajectory.inspectorTrimmed', entry.selection.trimmed],
      ])
    case 'mutation':
      return labeled(t, [
        ['kind', 'chat.trajectory.inspectorMutation', entry.mutation.kind],
        ['detail', 'chat.trajectory.inspectorDetail', entry.mutation.detail],
      ])
    case 'step':
      return labeled(t, [
        ['step', 'chat.trajectory.inspectorStep', String(entry.step.step_index ?? 0)],
        ['dropped', 'chat.trajectory.inspectorDropped', entry.step.dropped],
        ['truncated', 'chat.trajectory.inspectorTruncated', entry.step.truncated],
        ['outcome', 'chat.trajectory.inspectorOutcome', entry.step.reselection_outcome],
      ])
  }
}

type MemoryTrace = NonNullable<ContextfragLifecycleSnapshot['memory_recall']>

function memoryRows(memory: MemoryTrace): [string, string, string | number | null | undefined][] {
  return [
    ['provider', 'chat.trajectory.inspectorProvider', memory.provider_id],
    ['cacheState', 'chat.trajectory.inspectorCacheState', memory.cache_state],
    ['retrieval', 'chat.trajectory.inspectorRetrieval', memory.retrieval_mode],
    ['fallback', 'chat.trajectory.inspectorFallback', memory.fallback_reason],
    ['querySource', 'chat.trajectory.inspectorQuerySource', memory.query?.source],
    ['results', 'chat.trajectory.inspectorResults', String(memory.result?.count ?? 0)],
    ['contextBytes', 'chat.trajectory.inspectorBytes', memory.result?.context_bytes],
  ]
}

// The list an entry carries beyond its numbers: tool definitions, recalled
// memory refs, or the drop reasons of a selection.
export function contextListRows(entry: ContextEntry, snapshot: ContextfragLifecycleSnapshot | undefined, t: Translate): LabeledRow[] {
  switch (entry.kind) {
    case 'tool_defs':
      return (snapshot?.tool_defs ?? []).map((def, index) => ({
        key: `${def.provider ?? ''}/${def.name ?? ''}/${index}`,
        label: `${def.provider ?? ''}/${def.name ?? ''}`,
        value: formatTokenCount(def.token_estimate ?? 0),
        mono: true,
      }))
    case 'fragments':
    case 'memory_recall': {
      const memory = entry.kind === 'fragments' ? entry.memory : entry.memory
      return (memory?.result?.refs ?? []).map((ref, index) => ({ key: `${ref}/${index}`, label: ref, value: '', mono: true }))
    }
    case 'selection':
      return reasonRows(dropReasonRows(entry.selection), t)
    case 'step':
      return reasonRows(Object.entries(entry.step.drop_reasons ?? {}).map(([reason, count]) => ({ reason, count, tokens: null })), t)
    default:
      return []
  }
}

function reasonRows(rows: { reason: string, count: number, tokens: number | null }[], t: Translate): LabeledRow[] {
  return rows.map(row => ({
    key: row.reason,
    label: row.reason === 'unknown' ? t('chat.lifecycle.unknown') : row.reason,
    value: row.tokens == null ? String(row.count) : `${row.count} · ${formatTokenCount(row.tokens)}`,
    mono: true,
  }))
}

export type DecisionScope = 'system' | 'history' | 'cut'

// Which slice of the per-fragment audit explains a row, if any.
export function decisionScopeOf(row: TrajectoryRow): DecisionScope | null {
  const detail = row.detail
  if (detail.kind === 'system') return 'system'
  if (detail.kind !== 'context') return null
  if (detail.entry.kind === 'selection') return 'cut'
  if (detail.entry.kind === 'fragments' && detail.entry.fragmentKind === 'conversation_event') return 'history'
  return null
}

export interface StatsSegment {
  key: string
  params: Record<string, string>
}

// Groups follow DSH's stats line: a reading renders only with exact evidence,
// and a group with nothing sampled disappears rather than reading as zero.
export function statsSegments(stats: TrajectoryStats): StatsSegment[][] {
  const groups: StatsSegment[][] = []
  const counts: StatsSegment[] = [{ key: 'statsTurns', params: { n: String(stats.turns) } }]
  if (stats.steps > 0) counts.push({ key: 'statsSteps', params: { n: String(stats.steps) } })
  groups.push(counts)

  const wall: StatsSegment[] = []
  if (stats.llmMs > 0) wall.push({ key: 'statsLlm', params: { s: formatDurationMs(stats.llmMs) } })
  if (stats.toolMs > 0) wall.push({ key: 'statsTools', params: { s: formatDurationMs(stats.toolMs) } })

  const latency: StatsSegment[] = []
  if (stats.ttftAvgMs != null) latency.push({ key: 'statsTtft', params: { s: formatDurationMs(stats.ttftAvgMs) } })
  if (stats.decodeMs > 0 && stats.decodeTokens > 0) {
    latency.push({ key: 'statsTokPerSec', params: { n: String(Math.round(stats.decodeTokens / (stats.decodeMs / 1_000))) } })
  }

  const cache: StatsSegment[] = []
  if (stats.inputTokens > 0 && stats.cachedInputTokens > 0) {
    cache.push({ key: 'statsCacheHit', params: { p: String(Math.round((stats.cachedInputTokens / stats.inputTokens) * 100)) } })
  }

  const tokens: StatsSegment[] = []
  if (stats.inputTokens > 0) tokens.push({ key: 'statsInput', params: { n: formatTokenCount(stats.inputTokens) } })
  if (stats.outputTokens > 0) tokens.push({ key: 'statsOutput', params: { n: formatTokenCount(stats.outputTokens) } })

  for (const group of [wall, latency, cache, tokens]) {
    if (group.length) groups.push(group)
  }
  return groups
}

export const KIND_LABEL_KEY: Record<TrajectoryRowKind, string> = {
  system: 'chat.trajectory.kindSystem',
  user: 'chat.trajectory.kindUser',
  context: 'chat.trajectory.kindContext',
  assistant: 'chat.trajectory.kindAssistant',
  reasoning: 'chat.trajectory.kindReasoning',
  tool: 'chat.trajectory.kindTool',
  error: 'chat.trajectory.kindError',
  notice: 'chat.trajectory.kindNotice',
}

// Every class below is a literal so the Tailwind scanner can see it.
export const KIND_TONE_CLASS: Record<TrajectoryRowKind, string> = {
  system: 'text-accent-gray',
  user: 'text-accent-blue',
  context: 'text-accent-green',
  assistant: 'text-accent-purple',
  reasoning: 'text-accent-purple',
  tool: 'text-accent-orange',
  error: 'text-destructive',
  notice: 'text-warning',
}

export const KIND_BAR_CLASS: Record<TrajectoryRowKind, string> = {
  system: 'bg-accent-gray',
  user: 'bg-accent-blue',
  context: 'bg-accent-green',
  assistant: 'bg-accent-purple',
  reasoning: 'bg-accent-purple',
  tool: 'bg-accent-orange',
  error: 'bg-destructive',
  notice: 'bg-warning',
}

export const LANE_LABEL_KEY: Record<TimelineLane, string> = {
  input: 'chat.trajectory.laneInput',
  model: 'chat.trajectory.laneModel',
  tools: 'chat.trajectory.laneTools',
}

export const LANE_TTFT_CLASS = 'bg-accent-purple-soft-active'
