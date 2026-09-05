import type { CompactionLog, ContextfragLifecycleSnapshot, HandlersContextFragmentPreview } from '@memohai/sdk'
import { formatTokenCount } from './context-categories'
import { dropReasonRows } from './context-lifecycle-view'
import type { ContextEntry, FragmentRef, RowMapSegment, TimelineLane, TrajectoryRow, TrajectoryRowKind, TrajectoryStats } from './trajectory-model'
import { PREVIEW_SOURCE_CHARACTERS, previewText } from './trajectory-model'

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
  // Ledger rows the bar stands for: one, or a fold of consecutive turns
  // when the window holds more rows than the strip can show.
  rows: number
}

// The strip never mounts more bars than this; a longer window folds
// consecutive turns into one bar per lane.
export const MAX_STRIP_BARS = 400
const MIN_BAR_PCT = 1
const TURN_GAP_PCT = 0.6
const MAX_GAP_TOTAL_PCT = 20
const LANES: TimelineLane[] = ['input', 'model', 'tools']

type FoldedSegment = RowMapSegment & { rows: number }

// Folds keep ledger order: turns are grouped in runs of equal size, and each
// group yields one bar per lane that focuses the group's first row on that
// lane and sums its wall time.
function foldRowMap(segments: RowMapSegment[], maxBars: number): FoldedSegment[] {
  if (segments.length <= maxBars) return segments.map(segment => ({ ...segment, rows: 1 }))
  const turns: RowMapSegment[][] = []
  let last: string | null = null
  for (const segment of segments) {
    if (turns.length === 0 || segment.turnId !== last) {
      turns.push([])
      last = segment.turnId
    }
    turns[turns.length - 1]!.push(segment)
  }
  const turnsPerGroup = Math.max(1, Math.ceil((turns.length * LANES.length) / maxBars))
  const folded: FoldedSegment[] = []
  for (let start = 0; start < turns.length; start += turnsPerGroup) {
    const group = turns.slice(start, start + turnsPerGroup).flat()
    let groupStart = folded.length > 0
    for (const lane of LANES) {
      const members = group.filter(segment => segment.lane === lane)
      const first = members[0]
      if (!first) continue
      folded.push({
        key: `fold:${first.key}`,
        rowKey: first.rowKey,
        lane,
        kind: first.kind,
        turnId: first.turnId,
        turnStart: groupStart,
        durationMs: members.reduce((sum, segment) => sum + Math.max(segment.durationMs, 0), 0),
        splitMs: null,
        label: '',
        running: members.some(segment => segment.running),
        stepIndex: null,
        rows: members.length,
      })
      groupStart = false
    }
  }
  return folded
}

// Segments keep ledger order. Duration mode scales each by its own wall
// time with a floor so timeless rows stay visible; sequence mode gives every
// segment the same width. Turns are separated by a small gap whose total
// stays bounded, and the floor never exceeds an equal share, so a long
// window keeps every bar visible and duration mode stays proportional.
export function rowMapGeometry(segments: RowMapSegment[], mode: TimelineMode): RowMapBar[] {
  if (segments.length === 0) return []
  const folded = foldRowMap(segments, MAX_STRIP_BARS)
  const gaps = folded.filter((segment, index) => index > 0 && segment.turnStart).length
  const gapPct = gaps > 0 ? Math.min(TURN_GAP_PCT, MAX_GAP_TOTAL_PCT / gaps) : 0
  const available = 100 - gaps * gapPct
  const durations = folded.map(segment => Math.max(segment.durationMs, 0))
  const total = durations.reduce((sum, value) => sum + value, 0)
  const weights = mode === 'duration' && total > 0 ? durations.map(value => value / total) : folded.map(() => 1 / folded.length)
  const minPct = Math.min(MIN_BAR_PCT, available / folded.length)
  const floored = weights.map(weight => Math.max(weight * available, minPct))
  const scale = available / floored.reduce((sum, value) => sum + value, 0)
  let cursor = 0
  return folded.map((segment, index) => {
    if (index > 0 && segment.turnStart) cursor += gapPct
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
      rows: segment.rows,
    }
  })
}

// Rounding happens once, on the unit that is shown, so a value just under a
// boundary rolls over to the next unit instead of reading "1m 60s".
export function formatDurationMs(ms: number): string {
  const value = Math.max(ms, 0)
  if (value < 1_000) return `${Math.round(value)}ms`
  const tenths = Math.round(value / 100)
  if (tenths < 600) return `${(tenths / 10).toFixed(1)}s`
  const totalSeconds = Math.round(value / 1_000)
  const hours = Math.floor(totalSeconds / 3_600)
  const minutes = Math.floor((totalSeconds % 3_600) / 60)
  if (hours > 0) return `${hours}h ${minutes}m`
  return `${minutes}m ${totalSeconds % 60}s`
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

export type FragmentPreviews = Readonly<Record<string, HandlersContextFragmentPreview>>

// The row reads like DSH's CONTEXT rows: the head of the first fragment whose
// text the store kept, followed by how many more fragments the row covers.
export function fragmentRowPreview(refs: FragmentRef[], previews: FragmentPreviews | null | undefined): string | null {
  if (!previews) return null
  for (let index = 0; index < refs.length; index += 1) {
    const stored = previews[refs[index]!.textHash]
    if (!stored?.preview) continue
    const head = previewText(stored.preview, PREVIEW_SOURCE_CHARACTERS)
    const rest = refs.length - index - 1
    return rest > 0 ? `${head} (+${rest})` : head
  }
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
  compaction: 'chat.trajectory.kindCompaction',
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
  compaction: 'text-accent-purple',
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
  compaction: 'bg-accent-purple',
}

export const LANE_LABEL_KEY: Record<TimelineLane, string> = {
  input: 'chat.trajectory.laneInput',
  model: 'chat.trajectory.laneModel',
  tools: 'chat.trajectory.laneTools',
}

export const LANE_TTFT_CLASS = 'bg-accent-purple-soft-active'

export type PromptChangeKind = 'added' | 'removed' | 'changed'

export interface PromptChange {
  key: string
  label: string
  kind: string
  change: PromptChangeKind
  currentHash: string
  previousHash: string
}

interface PromptPart {
  key: string
  label: string
  kind: string
  hash: string
}

function promptParts(snapshot: ContextfragLifecycleSnapshot | undefined, previews: FragmentPreviews | null | undefined): PromptPart[] {
  const parts: PromptPart[] = []
  const seen = new Map<string, number>()
  const push = (kind: string, label: string, hash: string) => {
    const base = `${kind}\u0000${label}`
    const ordinal = seen.get(base) ?? 0
    seen.set(base, ordinal + 1)
    parts.push({ key: ordinal ? `${base}\u0000${ordinal}` : base, label, kind, hash })
  }
  ;(snapshot?.fragments ?? []).forEach((ref, index) => {
    const hash = ref.text_hash ?? ''
    push(ref.kind ?? '', previews?.[hash]?.label || `${ref.kind ?? ''}#${index}`, hash || ref.content_hash || '')
  })
  for (const def of snapshot?.tool_defs ?? []) {
    push('tool_definition', `${def.provider ?? ''}/${def.name ?? ''}`, def.content_hash ?? '')
  }
  return parts
}

// What the prompt of one run added, dropped, or rewrote relative to the run
// before it, fragment by fragment, from the hashes both snapshots carry.
export function promptFragmentChanges(
  current: ContextfragLifecycleSnapshot | undefined,
  previous: ContextfragLifecycleSnapshot | undefined,
  previews: FragmentPreviews | null | undefined,
): PromptChange[] {
  const before = new Map(promptParts(previous, previews).map(part => [part.key, part]))
  const changes: PromptChange[] = []
  for (const part of promptParts(current, previews)) {
    const prior = before.get(part.key)
    before.delete(part.key)
    if (!prior) {
      changes.push({ key: part.key, label: part.label, kind: part.kind, change: 'added', currentHash: part.hash, previousHash: '' })
    } else if (prior.hash !== part.hash) {
      changes.push({ key: part.key, label: part.label, kind: part.kind, change: 'changed', currentHash: part.hash, previousHash: prior.hash })
    }
  }
  for (const part of before.values()) {
    changes.push({ key: part.key, label: part.label, kind: part.kind, change: 'removed', currentHash: '', previousHash: part.hash })
  }
  return changes
}

export type DiffLine = { type: 'same' | 'add' | 'remove', text: string } | { type: 'skip', count: number }

export const MAX_DIFF_LINES = 400
const DIFF_CONTEXT_LINES = 2

// A line diff of two texts as an edit script, or null when either side is
// too long to compare cheaply; unchanged runs longer than the context fold
// into a skip marker.
export function lineDiff(before: string, after: string): DiffLine[] | null {
  const a = before.split('\n')
  const b = after.split('\n')
  if (a.length > MAX_DIFF_LINES || b.length > MAX_DIFF_LINES) return null
  const n = a.length
  const m = b.length
  const width = m + 1
  const lcs = new Uint16Array((n + 1) * width)
  for (let i = n - 1; i >= 0; i -= 1) {
    for (let j = m - 1; j >= 0; j -= 1) {
      lcs[i * width + j] = a[i] === b[j]
        ? lcs[(i + 1) * width + j + 1]! + 1
        : Math.max(lcs[(i + 1) * width + j]!, lcs[i * width + j + 1]!)
    }
  }
  const script: Extract<DiffLine, { text: string }>[] = []
  let i = 0
  let j = 0
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      script.push({ type: 'same', text: a[i]! })
      i += 1
      j += 1
    } else if (lcs[(i + 1) * width + j]! >= lcs[i * width + j + 1]!) {
      script.push({ type: 'remove', text: a[i]! })
      i += 1
    } else {
      script.push({ type: 'add', text: b[j]! })
      j += 1
    }
  }
  while (i < n) script.push({ type: 'remove', text: a[i++]! })
  while (j < m) script.push({ type: 'add', text: b[j++]! })
  if (!script.some(line => line.type !== 'same')) return script

  const out: DiffLine[] = []
  let run: Extract<DiffLine, { text: string }>[] = []
  const flush = (atEnd: boolean) => {
    const keepHead = out.length === 0 ? 0 : DIFF_CONTEXT_LINES
    const keepTail = atEnd ? 0 : DIFF_CONTEXT_LINES
    if (run.length <= keepHead + keepTail) {
      out.push(...run)
    } else {
      out.push(...run.slice(0, keepHead), { type: 'skip', count: run.length - keepHead - keepTail }, ...run.slice(run.length - keepTail))
    }
    run = []
  }
  for (const line of script) {
    if (line.type === 'same') {
      run.push(line)
      continue
    }
    flush(false)
    out.push(line)
  }
  flush(true)
  return out
}

function usageNumber(usage: unknown, key: string): number | undefined {
  if (!usage || typeof usage !== 'object') return undefined
  const value = (usage as Record<string, unknown>)[key]
  return typeof value === 'number' ? value : undefined
}

// The facts of one compaction in the inspector's label/value shape: what it
// replaced, when it ran, what the summarizer cost, and how it ended.
export function compactionDetailRows(compaction: CompactionLog, t: Translate, clock: (ms: number) => string): LabeledRow[] {
  const started = Date.parse(compaction.started_at ?? '')
  const ended = compaction.completed_at ? Date.parse(compaction.completed_at) : Number.NaN
  const status = compaction.status ?? ''
  const rows: LabeledRow[] = [
    { key: 'status', label: t('chat.trajectory.inspectorStatus'), value: status ? t(`chat.trajectory.compactionStatus.${status}`) : '' },
    { key: 'messages', label: t('chat.trajectory.inspectorMessagesCompacted'), value: String(compaction.message_count ?? 0) },
  ]
  if (compaction.anchor_start_ms && compaction.anchor_end_ms) {
    rows.push({ key: 'span', label: t('chat.trajectory.inspectorCoveredSpan'), value: `${clock(compaction.anchor_start_ms)} → ${clock(compaction.anchor_end_ms)}` })
  }
  if ((compaction.level ?? 0) > 0) rows.push({ key: 'level', label: t('chat.trajectory.inspectorLevel'), value: String(compaction.level) })
  if (Number.isFinite(started)) rows.push({ key: 'started', label: t('chat.trajectory.inspectorStarted'), value: clock(started) })
  if (Number.isFinite(ended)) {
    rows.push({ key: 'ended', label: t('chat.trajectory.inspectorEnded'), value: clock(ended) })
    rows.push({ key: 'duration', label: t('chat.trajectory.inspectorDuration'), value: formatDurationMs(ended - started) })
  }
  const input = usageNumber(compaction.usage, 'inputTokens')
  const output = usageNumber(compaction.usage, 'outputTokens')
  if (input) rows.push({ key: 'input', label: t('chat.trajectory.inspectorInputTokens'), value: formatTokenCount(input) })
  if (output) rows.push({ key: 'output', label: t('chat.trajectory.inspectorOutputTokens'), value: formatTokenCount(output) })
  if (compaction.superseded_at) rows.push({ key: 'superseded', label: t('chat.trajectory.inspectorSuperseded'), value: clock(Date.parse(compaction.superseded_at)) })
  if (compaction.error_message) rows.push({ key: 'error', label: t('chat.trajectory.inspectorError'), value: compaction.error_message })
  return rows
}
