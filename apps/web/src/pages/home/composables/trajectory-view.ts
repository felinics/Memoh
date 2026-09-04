import { formatTokenCount } from './context-categories'
import type { TimelineLane, TrajectoryRowKind, TrajectoryStats, TurnTimeline } from './trajectory-model'

export type TimelineMode = 'duration' | 'sequence'

export interface LaneBar {
  key: string
  lane: TimelineLane
  leftPct: number
  widthPct: number
  // Where the first token landed inside a model bar, as a percentage of the
  // bar's own width; null when the request never streamed a token.
  splitPct: number | null
  // Input bars scale their emphasis by tokens relative to the turn's largest
  // request; other lanes are always full.
  intensity: number
  label: string
  stepIndex: number | null
  start: number
  end: number
  tokens: number
  cachedTokens: number
}

const MIN_BAR_PCT = 1

function bar(span: TurnTimeline['spans'][number], leftPct: number, widthPct: number, intensity: number): LaneBar {
  const length = span.end - span.start
  const splitPct = span.ttftEnd != null && length > 0 ? Math.min(Math.max(((span.ttftEnd - span.start) / length) * 100, 0), 100) : null
  return {
    key: span.key,
    lane: span.lane,
    leftPct,
    widthPct: Math.max(widthPct, MIN_BAR_PCT),
    splitPct,
    intensity,
    label: span.label,
    stepIndex: span.stepIndex,
    start: span.start,
    end: span.end,
    tokens: span.tokens,
    cachedTokens: span.cachedTokens,
  }
}

export function laneGeometry(timeline: TurnTimeline, mode: TimelineMode): LaneBar[] {
  const maxInput = Math.max(1, ...timeline.spans.filter(span => span.lane === 'input').map(span => span.tokens))
  const intensityOf = (span: TurnTimeline['spans'][number]) => (span.lane === 'input' ? span.tokens / maxInput : 1)
  if (mode === 'duration') {
    const domain = Math.max(timeline.end - timeline.start, 1)
    return timeline.spans.map(span => bar(
      span,
      ((span.start - timeline.start) / domain) * 100,
      ((span.end - span.start) / domain) * 100,
      intensityOf(span),
    ))
  }
  const stepIndexes = [...new Set(timeline.spans.filter(span => span.stepIndex != null).map(span => span.stepIndex!))].sort((a, b) => a - b)
  const orphanTools = timeline.spans.some(span => span.stepIndex == null)
  const slots = Math.max(stepIndexes.length + (orphanTools ? 1 : 0), 1)
  const slotWidth = 100 / slots
  const slotOf = (stepIndex: number | null) => (stepIndex == null ? slots - 1 : Math.max(stepIndexes.indexOf(stepIndex), 0))
  const stepDomain = new Map<number | null, { start: number, end: number }>()
  for (const span of timeline.spans) {
    const current = stepDomain.get(span.stepIndex)
    stepDomain.set(span.stepIndex, {
      start: Math.min(current?.start ?? span.start, span.start),
      end: Math.max(current?.end ?? span.end, span.end),
    })
  }
  return timeline.spans.map((span) => {
    const slotLeft = slotOf(span.stepIndex) * slotWidth
    if (span.lane !== 'tools') return bar(span, slotLeft, slotWidth, intensityOf(span))
    const domain = stepDomain.get(span.stepIndex)!
    const length = Math.max(domain.end - domain.start, 1)
    return bar(
      span,
      slotLeft + ((span.start - domain.start) / length) * slotWidth,
      ((span.end - span.start) / length) * slotWidth,
      1,
    )
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

export const LANE_LABEL_KEY: Record<TimelineLane, string> = {
  input: 'chat.trajectory.laneInput',
  model: 'chat.trajectory.laneModel',
  tools: 'chat.trajectory.laneTools',
}

export const LANE_BAR_CLASS: Record<TimelineLane, string> = {
  input: 'bg-accent-blue',
  model: 'bg-accent-purple',
  tools: 'bg-accent-orange',
}

export const LANE_TTFT_CLASS = 'bg-accent-purple-soft-active'

export const LANE_INPUT_LIGHT_CLASS = 'bg-accent-blue-soft-active'
